package main

import (
	"net"
	"encoding/binary"
	"fmt"
	"time"
	"errors"
	"io/ioutil"
	"os"
) 

//Variables auxilio
var c clientCon
var arq []byte
var w janela = janela{0, 1, 0, 512,
					  1, 10000, 1, 512}
var pacotes map[int]*pacote
var acks map[uint32]int

type clientCon struct {
	idClient uint16
	conn     *net.UDPConn
	ackMsg   chan []byte
	arq  chan []byte
	finished chan bool
}
type janela struct {
	firstSend int
	lastSend  int
	eow       int
	expected  uint32
	CWND      int
	SSTHRESH  int
	increase  int
	cwndsize  int
}
type Cabecalho struct {
	seqNumber uint32
	ackNumber uint32
	id        uint16
	syn       bool
	ack       bool
	fin       bool
}

type pacote struct {
	Data []byte
	Send bool
}

func montarCabecalhoCliente(tipo int, seq uint32, id int) []byte {
	h := make([]byte, 12)
	if tipo == 1 {
		binary.LittleEndian.PutUint32(h[:4], seq)
		binary.LittleEndian.PutUint32(h[4:8], 0)
		binary.LittleEndian.PutUint16(h[8:10], uint16(id))
		binary.LittleEndian.PutUint16(h[10:12], 4)
		return h
	} else if tipo == 2 {
		binary.LittleEndian.PutUint32(h[:4], seq)
		binary.LittleEndian.PutUint32(h[4:8], 0)
		binary.LittleEndian.PutUint16(h[8:10], uint16(id))
		binary.LittleEndian.PutUint16(h[10:12], 2)
		return h
	} else {
		binary.LittleEndian.PutUint32(h[:4], seq)
		binary.LittleEndian.PutUint32(h[4:8], 0)
		binary.LittleEndian.PutUint16(h[8:10], uint16(id))
		binary.LittleEndian.PutUint16(h[10:12], 1)
		return h
	}
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
func DialTCP(ADDR string, PORT string) (*clientCon, error) {
	/* Resolve the server address */
	ServerAddr, err := net.ResolveUDPAddr("udp", ADDR+":"+PORT)
	if err != nil {
		return nil, err
	}
	/* Resolve the local address*/
	LocalAddr, err := net.ResolveUDPAddr("udp", "localhost:0")
	if err != nil {
		return nil, err
	}
	clientConn, err := net.DialUDP("udp", LocalAddr, ServerAddr)

	msg := montarCabecalhoCliente(1, 12345, 0)
	clientConn.Write(msg)
	c = clientCon{0, clientConn, make(chan []byte),
						 make(chan []byte), make(chan bool)}

	buf := make([]byte, 12)
	for c.idClient == 0 {
		n, _, err := clientConn.ReadFromUDP(buf)
		if err != nil {
			return nil, err
		}
		cabecalho := converterCabecalho(buf[:n])
		c.idClient = cabecalho.id
	}
	fmt.Println("Connection ok!")

	go listenServer()
	return &c, err
}
func (c *clientCon) sendMsg() {
	for data := range c.arq {
		if len(data) != 0 {
			_, err := c.conn.Write(data)
			if err != nil {
				fmt.Println(err)
			}
		}
	}
}
func (c *clientCon) Write(msg []byte) (int, error) {
	if len(msg) <= 100000000 {
		arq = make([]byte, len(msg))
		copy(arq, msg)
		pacotes = make(map[int]*pacote)
		acks = make(map[uint32]int)
		c.montarPacotes()
		
		go c.sendMsg()
		/******* Starting the select ********/
		c.arq<- pacotes[0].Data
		pacotes[0].Send = true
		count := 0
		finished := true
		for finished {
			select {
			case ack := <-c.ackMsg:
				h := converterCabecalho(ack)
				fmt.Println("Recebido", h.ackNumber, w.expected)
				if h.ack && !h.fin {
					/* Se um ack de um pacote que não é final foi recebido
					Então eu aumento o valor esperando pra a próxima posição do vetor*/
					if h.ackNumber == w.expected {
						count = 0
						w.expected += 512
						/* Verificação para aumentar a janela, se ela é maior que o SSTHRESH ou não */
						if w.cwndsize >= w.SSTHRESH {
							w.cwndsize += (512 * 512) / w.SSTHRESH
							fmt.Println(w.cwndsize, w.CWND, w.SSTHRESH, w.increase)
							if w.cwndsize-w.SSTHRESH >= w.increase*512 {
								w.CWND += 1
								w.cwndsize += 512
								w.increase += 1
							}
							/* Movimenta o FS pra posição a frente */
							w.firstSend = acks[h.ackNumber+511]
							w.eow = w.firstSend + w.CWND

							if w.firstSend >= w.lastSend {
								begin := w.firstSend
								end := w.eow
								for ; begin < end && begin < len(pacotes); begin++ {
									if pacotes[begin].Send != true {
										pacotes[begin].Send = true
										w.lastSend = begin
										c.arq <- pacotes[begin].Data
									}
								}
							} else {
								begin := w.lastSend
								end := w.eow
								for ; begin < end && begin < len(pacotes); begin++ {
									if pacotes[begin].Send != true {
										pacotes[begin].Send = true
										w.lastSend = begin
										c.arq <- pacotes[begin].Data
									}
								}
							}
						} else {
							/* Caso a janela não esteja maior que o SSTHRESH eu aumento normalmente */
							/* Aumenta a janela */
							w.CWND += 1
							/* Movimenta o FS pra posição a frente */
							w.firstSend = acks[h.ackNumber+511]
							/* Marca o limite da janela */
							w.eow = w.firstSend + w.CWND

							if w.firstSend >= w.lastSend {
								begin := w.firstSend
								end := w.eow
								for ; begin < end && begin < len(pacotes); begin++ {
									if pacotes[begin].Send != true {
										pacotes[begin].Send = true
										w.lastSend = begin
										c.arq<- pacotes[begin].Data
									}
								}
							} else {
								begin := w.lastSend
								end := w.eow
								for ; begin < end && begin < len(pacotes); begin++ {
									if pacotes[begin].Send != true {
										pacotes[begin].Send = true
										w.lastSend = begin
										c.arq <- pacotes[begin].Data
									}
								}
							}
						}
					} else if h.ackNumber > w.expected {
						/* aumenta o expected para o próximo depois do ack recebido, e o first também */
						for w.expected < h.ackNumber {
							w.expected += 512
						}
						w.firstSend = acks[w.expected-1]

						if w.firstSend >= w.lastSend {
							w.eow += w.CWND
							begin := w.lastSend
							end := w.eow
							for ; begin < end && begin < len(pacotes); begin++ {
								if pacotes[begin].Send != true {
									pacotes[begin].Send = true
									w.lastSend = begin
									c.arq <- pacotes[begin].Data
								}
							}
						} else {
							received := acks[h.ackNumber-1]
							increase := received - w.firstSend
							w.eow += increase
							begin := w.lastSend
							end := w.eow
							for ; begin < end && begin < len(pacotes); begin++ {
								if pacotes[begin].Send != true {
									pacotes[begin].Send = true
									w.lastSend = begin
									c.arq <- pacotes[begin].Data
								}
							}
						}
					}
				} else if h.fin {
					finished = false
					break
				}
				/* fim da primeira opção do select */
			case <-time.After(500 * time.Millisecond):
				count++
				if count < 20 {
					w.SSTHRESH = w.cwndsize
					w.cwndsize = 512
					w.increase = 1
					w.CWND = 1
					begin := acks[w.expected-1]
					w.firstSend = begin
					w.lastSend = begin + 30
					end := w.lastSend
					for ; begin < end && begin < len(pacotes); begin++ {
						w.lastSend = begin
						c.arq <- pacotes[begin].Data
					}
					break
				} else {
					finished = false
					return 0, errors.New("Error no servidor!")
				}
			}
		}
		//fmt.Println(w.CWND)
		return len(msg), nil
	} else {
		return 0, errors.New("Arquivo muito grande!")
	}
}

func (c *clientCon) montarPacotes() {
	i := 0
	k := 512
	j := 0
	for {
		if k > len(arq) {
			h := montarCabecalhoCliente(3, uint32(len(arq)-1), int(c.idClient))
			p := pacote{append(h, arq[i:]...), false}
			pacotes[j] = &p
			acks[uint32(k-1)] = j
			break
		} else {
			h := montarCabecalhoCliente(2, uint32(k-1), int(c.idClient))
			p := pacote{append(h, arq[i:k]...), false}
			pacotes[j] = &p
			acks[uint32(k-1)] = j
			i = k
			k += 512
			j++
		}
	}
}

func (c *clientCon) Read(msg []byte) (int, error) {
	for {
		n, _, err := c.conn.ReadFromUDP(msg)
		if n != 0 {
			return n, err
		}
	}
}
func (c *clientCon) Close() error {
	err := c.conn.Close()
	return err
}
func listenServer() {
	msg := make([]byte, 12)
	for {
		n, _, err := c.conn.ReadFromUDP(msg)
		if err != nil {
			fmt.Println(err)
		}
		c.ackMsg <- msg[:n]
	}
}

func main() {
	fmt.Println("Entre com o ip")
	ADDR := "localhost"
	//fmt.Scanln(&ADDR)
	fmt.Println("Digite a porta")
	PORT := "6666"
	//fmt.Scanln(&PORT)
	fmt.Println("Digite o nome do arquivo")
	FILENAME := "02.mp3"
	//fmt.Scanln(&FILENAME)
	//fmt.Scanln(&FILENAME)
	if ADDR == "" || PORT == "" || FILENAME == "" {
		fmt.Println("Argumentos vazios!")
	}

	conn, err := DialTCP(ADDR, PORT)
	if err != nil {
		fmt.Println(err)
	}

	file, err := ioutil.ReadFile(FILENAME)
	if err != nil {
		fmt.Println(err)
		os.Exit(1)
	}

	fmt.Println("Enviando...")
	_, err = conn.Write(file)
	if err != nil {
		fmt.Println(err)
	}
	fmt.Println("Finalizado")
}
