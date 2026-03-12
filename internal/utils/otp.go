package utils

import (
	cryptorand "crypto/rand"
	"fmt"
)

func GenerateOTP() string {
	b := make([]byte, 4)
	cryptorand.Read(b)
	return fmt.Sprintf("%08d", (int(b[0])<<24|int(b[1])<<16|int(b[2])<<8|int(b[3]))%100000000)
}
