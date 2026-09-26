package attachment

import (
	"encoding/binary"
	"encoding/json"
	"errors"
	"io"
	"net"
	"sync"
	"time"
)

const (
	carrierInit   byte = 1
	carrierInput  byte = 2
	carrierOutput byte = 3
	carrierResize byte = 4
	carrierReady  byte = 5
	carrierError  byte = 6
)
const maxCarrierFrame = 32768

type carrier struct {
	net.Conn
	writeMu sync.Mutex
}
type carrierFrame struct {
	kind byte
	data []byte
}
type initiation struct {
	Socket string `json:"socket"`
	Token  string `json:"token"`
	Width  int    `json:"width"`
	Height int    `json:"height"`
}
type size struct {
	Width  int `json:"width"`
	Height int `json:"height"`
}

func (c *carrier) read() (carrierFrame, error) {
	var header [5]byte
	if _, err := io.ReadFull(c.Conn, header[:]); err != nil {
		return carrierFrame{}, err
	}
	length := binary.BigEndian.Uint32(header[1:])
	if length > maxCarrierFrame {
		return carrierFrame{}, errors.New("attachment carrier frame too large")
	}
	f := carrierFrame{kind: header[0], data: make([]byte, length)}
	_, err := io.ReadFull(c.Conn, f.data)
	return f, err
}
func (c *carrier) write(kind byte, data []byte) error {
	if len(data) > maxCarrierFrame {
		return errors.New("attachment carrier frame too large")
	}
	c.writeMu.Lock()
	defer c.writeMu.Unlock()
	c.SetWriteDeadline(time.Now().Add(5 * time.Second))
	header := make([]byte, 5+len(data))
	header[0] = kind
	binary.BigEndian.PutUint32(header[1:5], uint32(len(data)))
	copy(header[5:], data)
	for len(header) > 0 {
		n, err := c.Conn.Write(header)
		if err != nil {
			return err
		}
		header = header[n:]
	}
	return nil
}
func (c *carrier) writeJSON(kind byte, v any) error {
	raw, err := json.Marshal(v)
	if err != nil {
		return err
	}
	return c.write(kind, raw)
}
