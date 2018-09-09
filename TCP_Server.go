package main
import (
	"sync"
	"fmt"
	"encoding/binary"
	"net"
	"time"
	"errors"
	"io/ioutil"
	"strconv"
    "os"
)
// Variáveis de auxilio
var idEnvio uint16 = 1
var listaDeConexoes map[string]*Conn
var mutex = &sync.RWMutex{}
var conexaoServidor *net.UDPConn
var canalConexoes chan newConn = make(chan newConn)

type Conn struct {
	Id                uint16
	ackResp           chan []byte
	channel           chan []byte
	Timeout           chan bool
	serverConnection  *net.UDPConn
	clientAddr        *net.UDPAddr
	seqNumberExpected uint32
	lastReceived      []byte
	finished          bool
}
type Cabecalho struct {
	seqNumber uint32
	ackNumber uint32
	id        uint16
	syn       bool
	ack       bool
	fin       bool
}
type newConn struct {
	addr *net.UDPAddr
	data []byte
}

func converterCabecalho(cabecalho []byte) Cabecalho {
	seqNumber := binary.LittleEndian.Uint32(cabecalho[:4])
	ackNumber := binary.LittleEndian.Uint32(cabecalho[4:8])
	idConnection := binary.LittleEndian.Uint16(cabecalho[8:10])
	flag := binary.LittleEndian.Uint16(cabecalho[10:12])
	h := Cabecalho{seqNumber, ackNumber, idConnection, false, false, false}

	if flag == 4 {
		h.syn = true
	} else if flag == 2 {
		h.ack = true
	} else if flag == 1 {
		h.fin = true
	} else if flag == 6 {
		h.ack = true
		h.syn = true
	} else if flag == 3 {
		h.ack = true
		h.fin = true
	}
	return h
}
//Criar vetor de bytes para enviar
func montarCabecalho(tipo int, cabecalho Cabecalho) []byte {
	if tipo == 1 {
		h := make([]byte, 12)
		binary.LittleEndian.PutUint32(h[:4], cabecalho.ackNumber)
		binary.LittleEndian.PutUint32(h[4:8], cabecalho.seqNumber+1)
		binary.LittleEndian.PutUint16(h[8:10], cabecalho.id)
		binary.LittleEndian.PutUint16(h[10:12], 6)
		return h
	} else if tipo == 2 {
		h := make([]byte, 12)
		binary.LittleEndian.PutUint32(h[:4], cabecalho.ackNumber)
		binary.LittleEndian.PutUint32(h[4:8], cabecalho.seqNumber+1)
		binary.LittleEndian.PutUint16(h[8:10], cabecalho.id)
		binary.LittleEndian.PutUint16(h[10:12], 2)
		return h
	} else {
		h := make([]byte, 12)
		binary.LittleEndian.PutUint32(h[:4], cabecalho.ackNumber)
		binary.LittleEndian.PutUint32(h[4:8], cabecalho.seqNumber+1)
		binary.LittleEndian.PutUint16(h[8:10], cabecalho.id)
		binary.LittleEndian.PutUint16(h[10:12], 3)
		return h
	}
}

func Listen(HOST string, PORT string) (*Conn, error) {
	
	if HOST == "" || PORT == "" {
		return nil, errors.New("Insira os valores do host e porta!")
	}
	ServerAddr, err := net.ResolveUDPAddr("udp", HOST+":"+PORT)
	if err != nil {
		return nil, err
	}
	ServerConn, err := net.ListenUDP("udp", ServerAddr)
	/* Initializing the connections map */
	listaDeConexoes = make(map[string]*Conn)
	conexaoServidor = ServerConn

	conn := Conn{0, make(chan []byte), make(chan []byte),
				 make(chan bool), conexaoServidor,
				 nil, 511, make([]byte, 12), false}
	go read()
	return &conn, err
}
func (c *Conn) Close() {
	c.serverConnection.Close()
}

func (c *Conn) Accept() (*Conn, error) {
	for pacote := range canalConexoes {
		connection := Conn{idEnvio, make(chan []byte), make(chan []byte),
						   make(chan bool), conexaoServidor, pacote.addr,
						   511, make([]byte, 12), false}
		mutex.Lock()
		listaDeConexoes[pacote.addr.String()] = &connection
		mutex.Unlock()
		data := montarCabecalho(1, Cabecalho{0, 0, idEnvio,
									  false, false, false })
		idEnvio++
		connection.seqNumberExpected = 511
		connection.finished = false
		_, err := connection.serverConnection.WriteToUDP(data, pacote.addr)
		if err != nil {
			fmt.Println(err)
		}

		go connection.ackResponse()
		go connection.timeout()
		return &connection, err
	}
	return &Conn{}, nil
}
func (c *Conn) Write(msg []byte) (int, error) {
	n, err := c.serverConnection.WriteTo(msg, c.clientAddr)
	return n, err
}
func (c *Conn) timeout() {
	stop := false
	for !stop {
		select {
		case <-time.After(10 * time.Second):
			fmt.Println("Timeout!", c.clientAddr)
			c.channel <- []byte("error")
			c.finished = false
			stop = true
			delete(listaDeConexoes, c.clientAddr.String())
			break
		case t := <-c.Timeout:
			if !t {
				stop = true
				break
			}
		}
	}
}
/* Trata dados lidos pelo servidor*/
func (c *Conn) Read(msg []byte) (int, error) {
	for data := range c.channel {
		if string(data[:5]) == "error" {
			return 0, errors.New("Error")
		}
		h := converterCabecalho(data[:12])
		if h.ack {
			if h.seqNumber == c.seqNumberExpected {
				c.seqNumberExpected += 512
				copy(msg, data[12:])
				lastHeader := montarCabecalho(2, h)
				copy(c.lastReceived, lastHeader)
				c.ackResp <- lastHeader
				c.Timeout <- true
				return len(data) - 12, nil
			} else {
				c.ackResp <- c.lastReceived
				return 0, nil
			}
		} else if h.fin {
			if c.finished == false {
				c.finished = true
				c.seqNumberExpected -= uint32(512 - len(data))
				copy(msg, data[12:])
				copy(c.lastReceived, data[:12])
				cabecalho := converterCabecalho(c.lastReceived)
				byteHeader := montarCabecalho(3, cabecalho)
				c.ackResp <- byteHeader
				c.Timeout <- false
				delete(listaDeConexoes, c.clientAddr.String())
				return len(data) - 12, nil
			} else {
				c.ackResp <- c.lastReceived
				return 0, nil
			}
		}
	}
	return 0, nil
}
func (c *Conn) ackResponse() {
	for data := range c.ackResp {
		//fmt.Println("Enviando", converterCabecalho(data))
		//ack := rand.Int()
		//if ack%10 == 0 {
		//	fmt.Println("Perdi")
		//} else {
		//fmt.Println(converterCabecalho(data))
		_, err := c.serverConnection.WriteToUDP(data, c.clientAddr)
		if err != nil {
			fmt.Println(err)
		}
		//}
	}
}
func (c *Conn) Finished() bool {
	return c.finished
}

func read() {
	msg := make([]byte, 524)
	for {
		n, addr, _ := conexaoServidor.ReadFromUDP(msg)
		h := converterCabecalho(msg[:12])
		if h.syn && h.ackNumber == 0 && h.id == 0 {
			data := make([]byte, n)
			copy(data, msg[:n])
			pacote := newConn{addr, msg}
			canalConexoes <- pacote
		} else if h.ackNumber == 0 {
			data := make([]byte, n)
			copy(data, msg[:n])
			if len(data) != 0 {
				mutex.Lock()
				if listaDeConexoes[addr.String()] != nil {
					listaDeConexoes[addr.String()].channel <- data
				}
				mutex.Unlock()
			}
		}
	}
}

var DIRETORIO string

func criarDiretorio(diretorio string) {
	err := os.Mkdir(diretorio, 0777)
	if os.IsExist(err) {
		op := 0
		fmt.Println("diretorio já exite")
		fmt.Println("1. Renomear")
		fmt.Println("2. Substituir")
		fmt.Scanf("%d", &op)
		if op == 1 {
			fmt.Scanln(DIRETORIO)
			criarDiretorio(DIRETORIO)
		} else if op == 2 {
			fmt.Println("Substituido!")
		} else {
			fmt.Println("opção invalida!")
			os.Exit(1)
		}
	} else {
		fmt.Println("Criou!")
	}
}
func writeClientFile(conn *Conn) {
	fmt.Println("Nova Conexão Aceita")
	var total []byte
	msg := make([]byte, 524)
	gaveError := false
	for !conn.Finished() {
		n, err := conn.Read(msg)
		if err != nil && n == 0{
			gaveError = true
			fmt.Println(err)
			break
		} else {
			total = append(total, msg[:n]...)
		}
	}
	if gaveError {
		fmt.Println("Error")
		ioutil.WriteFile(DIRETORIO+"/"+strconv.Itoa(int(conn.Id))+"ERROR", total, 0777)
	} else {
		ioutil.WriteFile(DIRETORIO+"/"+strconv.Itoa(int(conn.Id))+".FILE", total, 0777)
		fmt.Println("Finalizou!", DIRETORIO)
	}
}

func main() {
	fmt.Println("Insira a porta")
	PORT := "6666"
	fmt.Scanln(&PORT)
	fmt.Println("Insira o nome da pasta para salvar o arquivo")
	DIRETORIO = "save"
	fmt.Scanln(&DIRETORIO)
	if DIRETORIO == "" {
		fmt.Println("Nome do diretorio vazio")
		os.Exit(1)
	}

	conn, err := Listen("localhost", PORT)
	if err != nil {
		fmt.Println(err)
		os.Exit(1)
	}
	defer conn.Close()

	criarDiretorio(DIRETORIO)

	for {
		c, err := conn.Accept()
		if err != nil {
			fmt.Println("Error:", err)
			os.Exit(1)
		}
		go writeClientFile(c)
	}
}
