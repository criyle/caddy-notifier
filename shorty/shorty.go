package shorty

import (
	"bytes"
	"strings"
)

// Node represents a node in the Huffman tree.
type Node struct {
	symbol string // The symbol (token) stored in the node, if it's a leaf
	up     int    // Index of the parent node
	weight int    // Frequency weight of the node
}

// Shorty implements adaptive Huffman compression and decompression.
type Shorty struct {
	data []byte // Encoded binary data as a string

	nodes     []Node // Huffman tree nodes
	nyt       int    // Index of the NYT (Not Yet Transmitted) node
	tokenSize int    // Maximum token size for grouping characters

	curPos   int  // Current bit position while reading
	bitCount int  // Bit counter for writing bits
	bitChar  byte // Current byte being constructed from bits
}

// NewShorty creates a new Shorty instance with a given token size.
func NewShorty(tokenSize int) *Shorty {
	s := &Shorty{tokenSize: tokenSize}
	s.Reset(true)
	return s
}

// Reset initializes or resets the state of the Shorty instance.
func (s *Shorty) Reset(full bool) {
	if full {
		if s.nodes == nil {
			s.nodes = []Node{{up: 0, weight: 0}}
		} else {
			s.nodes = s.nodes[:1]
			s.nodes[0] = Node{up: 0, weight: 0}
		}
		s.nyt = 0
	}
	if s.data != nil {
		s.data = s.data[:0]
	} else {
		s.data = make([]byte, 0)
	}
	s.curPos = 0
	s.bitCount = 7
	s.bitChar = 0
}

// findNode searches for a node with the given symbol.
func (s *Shorty) findNode(x string) int {
	for i := len(s.nodes) - 1; i > 0; i-- {
		if s.nodes[i].symbol == x {
			return i
		}
	}
	return 0
}

// addNode adds a new symbol to the Huffman tree.
func (s *Shorty) addNode(token string) int {
	if len(s.nodes) >= 2046 {
		return 0
	}
	s.nodes = append(s.nodes, Node{up: s.nyt, symbol: token, weight: 1})
	s.nodes = append(s.nodes, Node{up: s.nyt, weight: 0})
	s.nodes[s.nyt].weight++
	s.nyt = len(s.nodes) - 1
	if s.nodes[len(s.nodes)-3].up != len(s.nodes)-3 {
		s.balanceNode(s.nodes[len(s.nodes)-3].up)
	}
	return len(s.nodes) - 3
}

// swapNode swaps two nodes in the tree.
func (s *Shorty) swapNode(a, b int) {
	t := s.nodes[a].symbol
	u := s.nodes[b].symbol
	v := s.nodes[a].weight
	s.nodes[a].symbol = u
	s.nodes[b].symbol = t
	s.nodes[a].weight = s.nodes[b].weight
	s.nodes[b].weight = v
	for n := len(s.nodes) - 1; n > 0; n-- {
		if s.nodes[n].up == a {
			s.nodes[n].up = b
		} else if s.nodes[n].up == b {
			s.nodes[n].up = a
		}
	}
}

// balanceNode updates the tree to maintain Huffman properties.
func (s *Shorty) balanceNode(node int) {
	for {
		minnr := node
		weight := s.nodes[node].weight
		for minnr > 1 && s.nodes[minnr-1].weight == weight {
			minnr--
		}
		if minnr != node && minnr != s.nodes[node].up {
			s.swapNode(minnr, node)
			node = minnr
		}
		s.nodes[node].weight++
		if s.nodes[node].up == node {
			return
		}
		node = s.nodes[node].up
	}
}

// emitNode emits the path from a node to the root as bits.
func (s *Shorty) emitNode(node int) {
	emit := make([]byte, 0, 16)
	for node != 0 {
		emit = append(emit, byte(node%2))
		node = s.nodes[node].up
	}
	// Emit bits in reverse order
	for i := len(emit) - 1; i >= 0; i-- {
		s.emitBit(emit[i])
	}
}

// emitNyt emits the NYT node and the new token.
func (s *Shorty) emitNyt(token string) int {
	s.emitNode(s.nyt)
	ll := byte(len(token) - 1)
	if s.tokenSize > 8 {
		s.emitBit(ll & 8)
	}
	if s.tokenSize > 4 {
		s.emitBit(ll & 4)
	}
	if s.tokenSize > 2 {
		s.emitBit(ll & 2)
	}
	if s.tokenSize > 1 {
		s.emitBit(ll & 1)
	}
	for _, cc := range []byte(token) {
		s.emitByte(cc)
	}
	return s.nyt
}

// readTokenLength reconstruct length from bits
func (s *Shorty) readTokenLength() (int, bool) {
	length := 0
	if s.tokenSize > 8 {
		bit, ok := s.readBit()
		if !ok {
			return 0, false
		}
		length += int(bit * 8)
	}
	if s.tokenSize > 4 {
		bit, ok := s.readBit()
		if !ok {
			return 0, false
		}
		length += int(bit * 4)
	}
	if s.tokenSize > 2 {
		bit, ok := s.readBit()
		if !ok {
			return 0, false
		}
		length += int(bit * 2)
	}
	if s.tokenSize > 1 {
		bit, ok := s.readBit()
		if !ok {
			return 0, false
		}
		length += int(bit)
	}
	return length + 1, true
}

func (s *Shorty) readToken() string {
	length, ok := s.readTokenLength()
	if !ok {
		return ""
	}

	stream := make([]byte, 0, length)
	for length > 0 {
		b, ok := s.readByte()
		if !ok {
			return ""
		}
		stream = append(stream, b)
		length--
	}
	return string(stream)
}

// readNode reads a symbol from the encoded data.
func (s *Shorty) readNode() string {
	if s.nyt == 0 {
		return s.readToken()
	}

	node := 0
	for {
		bit, ok := s.readBit()
		if !ok {
			return ""
		}

		// Traverse the tree based on the bit
		if s.nodes[node].symbol == "" {
			for m := 0; ; m++ {
				if s.nodes[m].up == node && m != node && (m%2 == int(bit)) {
					node = m
					break
				}
			}
		}

		// If we reach a leaf or NYT node
		if s.nodes[node].symbol != "" || s.nodes[node].weight == 0 {
			if s.nodes[node].weight != 0 {
				return s.nodes[node].symbol
			}
			return s.readToken()
		}
	}
}

// emitBit writes a single bit to the output.
func (s *Shorty) emitBit(bit byte) {
	if bit != 0 {
		s.bitChar += 1 << s.bitCount
	}
	if s.bitCount--; s.bitCount < 0 {
		s.data = append(s.data, byte(s.bitChar))
		s.bitCount = 7
		s.bitChar = 0
	}
}

// emitByte writes a full byte to the output.
func (s *Shorty) emitByte(b byte) {
	for i := 7; i >= 0; i-- {
		s.emitBit(b >> i & 1)
	}
}

// readBit reads a single bit from the input.
func (s *Shorty) readBit() (byte, bool) {
	if s.curPos >= len(s.data)*8 {
		return 0, false // No more bits to read
	}
	bit := byte(s.data[s.curPos>>3]) >> (7 - s.curPos&7) & 1
	s.curPos++
	return bit, true
}

// readByte reads a full byte from the input.
func (s *Shorty) readByte() (byte, bool) {
	var res byte = 0
	for i := range 8 {
		bit, ok := s.readBit() // No more bytes to read
		if !ok {
			return 0, false
		}
		res += (128 >> i) * bit
	}
	return res, true
}

func isAlpha(c byte) bool {
	return ('a' <= c && c <= 'z') || ('A' <= c && c <= 'Z')
}

func isPunctGroup(c byte) bool {
	switch c {
	case '=', '[', ']', ',', '.', ':', '"', '\'', '{', '}':
		return true
	default:
		return false
	}
}

// deflate compresses the input string using adaptive Huffman coding.
func (s *Shorty) deflate(data []byte) []byte {
	l := len(data)
	s.Reset(false)
	for i := 0; i < l; i++ {
		tokenBuilder := strings.Builder{}
		tokenBuilder.WriteByte(data[i])

		if s.tokenSize > 1 {
			if isAlpha(data[i]) {
				for i+1 < l && tokenBuilder.Len() < s.tokenSize && isAlpha(data[i+1]) {
					i++
					tokenBuilder.WriteByte(data[i])
				}
			} else if isPunctGroup(data[i]) {
				for i+1 < l && tokenBuilder.Len() < s.tokenSize && isPunctGroup(data[i+1]) {
					i++
					tokenBuilder.WriteByte(data[i])
				}
			}
		}
		token := tokenBuilder.String()

		x := s.findNode(token)
		if x == 0 {
			s.emitNyt(token)
			s.addNode(token)
		} else {
			s.emitNode(x)
			s.balanceNode(x)
		}
	}
	if s.bitCount != 7 {
		// oldLength := len(data)
		s.emitNode(s.nyt)
		// if oldLength == len(data) {
		s.emitByte(0)
		// }
	}
	return s.data
}

// deflate decompresses the input string using adaptive Huffman coding.
func (s *Shorty) inflate(data []byte) []byte {
	s.Reset(false)
	s.data = data
	output := new(bytes.Buffer)

	for {
		token := s.readNode()
		if token == "" {
			break // End of stream
		}
		output.WriteString(token)

		node := s.findNode(token)
		if node == 0 {
			s.addNode(token)
		} else {
			s.balanceNode(node)
		}
	}
	return output.Bytes()
}
