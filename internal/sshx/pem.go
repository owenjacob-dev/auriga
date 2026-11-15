package sshx

import "encoding/pem"

// pemDecode returns the body of the first PEM block, or nil.
func pemDecode(b []byte) []byte {
	block, _ := pem.Decode(b)
	if block == nil {
		return nil
	}
	return block.Bytes
}
