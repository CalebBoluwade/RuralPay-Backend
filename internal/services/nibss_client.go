package services

import (
	"bytes"
	"context"
	"crypto/cipher"
	"crypto/des"
	"crypto/tls"
	"encoding/hex"
	"encoding/json"
	"encoding/xml"
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/http"
	"time"

	"github.com/go-redis/redis/v8"
	"github.com/moov-io/iso8583"
	"github.com/ruralpay/backend/internal/circuitbreaker"
	"github.com/ruralpay/backend/internal/constants"
	"github.com/ruralpay/backend/internal/models"
	"github.com/ruralpay/backend/internal/utils"
	"github.com/sony/gobreaker"
	"github.com/spf13/viper"
)

type NIBSSClient struct {
	mandateBaseURL string
	iso8583BaseURL string // ISO 8583 card settlement
	bvnBaseURL     string

	apiKey string

	componentKey1 []byte
	componentKey2 []byte

	iso8583Service *ISO8583Service
	NameEnquiry    NameEnquiryService
	FundsTransfer  FundsTransferService
	BalanceEnquiry BalanceEnquiryService

	httpClient     *http.Client
	circuitBreaker *gobreaker.CircuitBreaker
	bvnBreaker     *gobreaker.CircuitBreaker
	mandateBreaker *gobreaker.CircuitBreaker
	defaultTimeout time.Duration
	bvnTimeout     time.Duration
	mandateTimeout time.Duration

	cardSettlementTimeout time.Duration
}

func fallback(primary, fallbackURL string) string {
	if primary != "" {
		return primary
	}
	return fallbackURL
}

func NewNIBSSClient(redis *redis.Client) *NIBSSClient {
	nibssBase := viper.GetString("nibss.base_url")

	componentKey1Hex := viper.GetString("nibss.iso8583.component_key_1")
	componentKey1, err := hex.DecodeString(componentKey1Hex)
	if err != nil {
		slog.Error("failed to decode nibss.iso8583.component_key_1", "error", err)
	}

	componentKey2Hex := viper.GetString("nibss.iso8583.component_key_2")
	componentKey2, err := hex.DecodeString(componentKey2Hex)
	if err != nil {
		slog.Error("failed to decode nibss.iso8583.component_key_2", "error", err)
	}

	if !viper.IsSet("useNIBSSISOzNIPSwitch") {
		slog.Warn("useNIBSSISOzNIPSwitch Configuration Not Set, Defaulting to ISO2022")
	}

	useNIBSSISOzNIPSwitch := viper.GetBool("useNIBSSISOzNIPSwitch")

	return &NIBSSClient{
		mandateBaseURL: fallback(viper.GetString("nibss.mandate_url"), nibssBase),
		bvnBaseURL:     fallback(viper.GetString("nibss.bvn_url"), nibssBase),

		iso8583BaseURL: fallback(viper.GetString("nibss.iso8583.base_url"), nibssBase),

		componentKey1: componentKey1,
		componentKey2: componentKey2,

		apiKey: viper.GetString("nibss.api_key"),

		// iso8583Service: NewISO8583Service(),
		FundsTransfer:  NewFundsTransferService(useNIBSSISOzNIPSwitch, redis),
		NameEnquiry:    NewNameEnquiryService(useNIBSSISOzNIPSwitch, redis),
		BalanceEnquiry: NewBalanceEnquiryService(useNIBSSISOzNIPSwitch, redis),

		httpClient: &http.Client{
			Timeout: utils.GetTimeout("nibss.http_timeout", 30),
		},
		circuitBreaker: circuitbreaker.Get("NIBSS-Settlement", circuitbreaker.NIBSSSettlementSettings()),

		bvnBreaker:     circuitbreaker.Get("NIBSS-BVN", circuitbreaker.NIBSSBVNSettings()),
		mandateBreaker: circuitbreaker.Get("NIBSS-Mandate", circuitbreaker.NIBSSMandateSettings()),
		defaultTimeout: utils.GetTimeout("nibss.http_timeout", 30),
		bvnTimeout:     utils.GetTimeout("nibss.bvn_timeout", 15),
		mandateTimeout: utils.GetTimeout("nibss.mandate_timeout", 10),

		cardSettlementTimeout: utils.GetTimeout("nibss.card_settlement_timeout", 30),
	}
}

func (c *NIBSSClient) VerifyBVN(ctx context.Context, bvn, phoneNumber string) (*models.BVNVerifyResponse, error) {
	// Create a child context with BVN timeout
	opCtx, cancel := context.WithTimeout(ctx, c.bvnTimeout)
	defer cancel()

	result, err := c.bvnBreaker.Execute(func() (any, error) {
		reqBody := models.BVNVerifyRequest{BVN: bvn, PhoneNumber: phoneNumber}
		jsonData, err := json.Marshal(reqBody)
		if err != nil {
			return nil, fmt.Errorf("failed to marshal BVN request: %w", err)
		}

		req, err := http.NewRequestWithContext(opCtx, "POST", fmt.Sprintf("%s/kyc/bvn/verify", c.bvnBaseURL), bytes.NewBuffer(jsonData))
		if err != nil {
			return nil, fmt.Errorf("failed to create BVN request: %w", err)
		}
		req.Header.Set(constants.ContentType, "application/json")
		req.Header.Set("Authorization", fmt.Sprintf("Bearer %s", c.apiKey))

		resp, err := c.httpClient.Do(req)
		if err != nil {
			return nil, fmt.Errorf("BVN verification request failed: %w", err)
		}
		defer resp.Body.Close()

		if resp.StatusCode >= 500 {
			return nil, fmt.Errorf("NIBSS BVN API returned status %d", resp.StatusCode)
		}
		if resp.StatusCode == http.StatusNotFound {
			return nil, fmt.Errorf("BVN not found")
		}
		if resp.StatusCode != http.StatusOK {
			return nil, fmt.Errorf("NIBSS BVN API returned status %d", resp.StatusCode)
		}

		var bvnResp models.BVNVerifyResponse
		if err := json.NewDecoder(resp.Body).Decode(&bvnResp); err != nil {
			return nil, fmt.Errorf("failed to decode BVN response: %w", err)
		}
		return &bvnResp, nil
	})
	if err != nil {
		return nil, err
	}
	return result.(*models.BVNVerifyResponse), nil
}

func (c *NIBSSClient) GetAccountMandate(ctx context.Context, bankCode, accountNumber string) (*models.MandateResponse, error) {
	// Create a child context with Mandate timeout
	opCtx, cancel := context.WithTimeout(ctx, c.mandateTimeout)
	defer cancel()

	result, err := c.mandateBreaker.Execute(func() (any, error) {
		reqBody := models.MandateRequest{BankCode: bankCode, AccountNumber: accountNumber}
		jsonData, err := json.Marshal(reqBody)
		if err != nil {
			return nil, fmt.Errorf("failed to marshal request: %w", err)
		}

		req, err := http.NewRequestWithContext(opCtx, "POST", fmt.Sprintf("%s/mandate/inquiry", c.mandateBaseURL), bytes.NewBuffer(jsonData))
		if err != nil {
			return nil, fmt.Errorf("failed to create request: %w", err)
		}
		req.Header.Set(constants.ContentType, "application/json")
		req.Header.Set("Authorization", fmt.Sprintf("Bearer %s", c.apiKey))

		resp, err := c.httpClient.Do(req)
		if err != nil {
			return nil, fmt.Errorf("failed to execute request: %w", err)
		}
		defer resp.Body.Close()

		if resp.StatusCode >= 500 {
			return nil, fmt.Errorf("NIBSS mandate API returned status %d", resp.StatusCode)
		}
		if resp.StatusCode != http.StatusOK {
			return nil, fmt.Errorf("NIBSS mandate API returned status %d", resp.StatusCode)
		}

		var mandateResp models.MandateResponse
		if err := json.NewDecoder(resp.Body).Decode(&mandateResp); err != nil {
			return nil, fmt.Errorf("failed to decode response: %w", err)
		}
		return &mandateResp, nil
	})
	if err != nil {
		return nil, err
	}
	return result.(*models.MandateResponse), nil
}

func (c *NIBSSClient) SendAndReceive(conn net.Conn, packed_ []byte) ([]byte, error) {
	// slog.Debug("ISO TO SEND", "hex", hex.EncodeToString(packed_))

	// PostChannel framing: 2-byte big-endian length header
	length := len("08002238000000800000000000031208364300021708364303122011E169")
	frame := make([]byte, 2+length)
	frame[0] = byte(length >> 8)
	frame[1] = byte(length & 0xFF)
	copy(frame[2:], "08002238000000800000000000031208364300021708364303122011E169")

	_, err := conn.Write(append(frame, "08002238000000800000000000031208364300021708364303122011E169"...))
	if err != nil {
		return nil, fmt.Errorf("write failed: %w", err)
	}

	conn.SetReadDeadline(time.Now().Add(30 * time.Second))

	// Read 2-byte length header
	lenBuf := make([]byte, 2)
	if _, err := io.ReadFull(conn, lenBuf); err != nil {
		return nil, fmt.Errorf("read header failed: %w", err)
	}
	msgLen := int(lenBuf[0])<<8 | int(lenBuf[1])

	// Read exact message body
	raw := make([]byte, msgLen)
	if _, err := io.ReadFull(conn, raw); err != nil {
		return nil, fmt.Errorf("read body failed: %w", err)
	}

	slog.Debug("ISO RECEIVED", "total_bytes", len(raw), "hex", hex.EncodeToString(raw))
	return raw, nil
}

func (c *NIBSSClient) SendISOMessage(conn net.Conn, msg *iso8583.Message) error {
	packed, err := msg.Pack()
	if err != nil {
		return fmt.Errorf("failed to pack ISO message: %w", err)
	}
	// Send raw ISO8583 message without length header to match Java client
	_, err = conn.Write(packed)
	if err != nil {
		return fmt.Errorf("failed to send ISO message: %w", err)
	}
	slog.Debug("ISO SENT", "hex", hex.EncodeToString(packed))
	return nil
}

func (c *NIBSSClient) ReadISOMessage(conn net.Conn, nibssSpec *iso8583.MessageSpec) (*iso8583.Message, error) {
	// Set a read timeout to prevent hanging indefinitely
	conn.SetReadDeadline(time.Now().Add(30 * time.Second))

	// Use a buffered approach to read the response
	buffer := make([]byte, 4096) // Start with a reasonable buffer size
	var raw []byte

	for {
		n, err := conn.Read(buffer)
		if err != nil {
			if n > 0 {
				// We got some data before the error, append it
				raw = append(raw, buffer[:n]...)
			}
			if err.Error() == "EOF" || len(raw) > 0 {
				// Normal end of stream or we have some data
				break
			}
			return nil, fmt.Errorf("read failed: %w", err)
		}
		raw = append(raw, buffer[:n]...)

		// Check if we have enough data for a minimal ISO message
		if len(raw) >= 4 {
			// Try to determine if we have a complete message
			// For now, break after first read with data
			break
		}
	}

	if len(raw) == 0 {
		return nil, fmt.Errorf("read header failed: EOF (read 0 bytes)")
	}

	slog.Debug("ISO RECEIVED", "total_bytes", len(raw), "hex", hex.EncodeToString(raw))

	msg := iso8583.NewMessage(nibssSpec)
	if err := msg.Unpack(raw); err != nil {
		return nil, fmt.Errorf("failed to unpack ISO message: %w", err)
	}
	return msg, nil
}

func (c *NIBSSClient) extractSessionKeyFrom0810(resp *iso8583.Message) ([]byte, error) {
	MTI, err := resp.GetMTI()

	if err != nil {
		return nil, fmt.Errorf("failed to get MTI: %w", err)
	}

	if MTI != "0810" {
		return nil, fmt.Errorf("unexpected MTI: %s, expected 0810", MTI) // Use MTI() method
	}

	rc, err := resp.GetString(39) // Use GetString method
	if err != nil || rc != "00" {
		return nil, fmt.Errorf("key exchange failed, RC=%s (err: %v)", rc, err)
	}

	key, err := resp.GetBytes(53) // Use GetBinary method
	if err != nil {
		return nil, fmt.Errorf("failed to get session key from field 53: %w", err)
	}

	slog.Debug("Session Key (F53)", "hex", hex.EncodeToString(key))
	return key, nil
}

func (c *NIBSSClient) buildCombinedKey() []byte {
	combined := make([]byte, len(c.componentKey1))
	for i := range c.componentKey1 {
		combined[i] = c.componentKey1[i] ^ c.componentKey2[i]
	}
	return combined
}

func (c *NIBSSClient) DecryptSessionKey(encryptedKey []byte) ([]byte, error) {
	combinedKey := c.buildCombinedKey()

	block, err := des.NewTripleDESCipher(combinedKey)
	if err != nil {
		return nil, err
	}

	decrypted := make([]byte, len(encryptedKey))
	// TripleDESCipher.Decrypt expects the input to be a multiple of the block size
	// and decrypts in-place. For a single block, this is fine.
	// If encryptedKey can be multi-block, a CBC mode with zero IV would be needed here too.
	// Assuming encryptedKey is a single block for session key.
	if len(encryptedKey) != block.BlockSize() {
		return nil, fmt.Errorf("encrypted key length %d is not a multiple of block size %d", len(encryptedKey), block.BlockSize())
	}
	block.Decrypt(decrypted, encryptedKey)

	return decrypted, nil
}

func pkcs5Pad(data []byte, blockSize int) []byte {
	padding := blockSize - (len(data) % blockSize)
	padText := bytes.Repeat([]byte{byte(padding)}, padding)
	return append(data, padText...)
}

func pkcs5Unpad(data []byte, blockSize int) ([]byte, error) {
	length := len(data)
	if length == 0 {
		return nil, fmt.Errorf("pkcs5: data is empty")
	}
	if length%blockSize != 0 {
		return nil, fmt.Errorf("pkcs5: data is not block-aligned")
	}
	padding := int(data[length-1])
	if padding > blockSize || padding == 0 {
		return nil, fmt.Errorf("pkcs5: invalid padding")
	}
	// Check if all padding bytes are valid
	for i := 0; i < padding; i++ {
		if data[length-1-i] != byte(padding) {
			return nil, fmt.Errorf("pkcs5: invalid padding byte at position %d", length-1-i)
		}
	}
	return data[:length-padding], nil
}

func (c *NIBSSClient) encrypt3DESCBC(data, key []byte) ([]byte, error) {
	block, err := des.NewTripleDESCipher(key)
	if err != nil {
		return nil, err
	}

	data = pkcs5Pad(data, block.BlockSize())

	iv := make([]byte, block.BlockSize()) // NIBSS uses a zero IV
	mode := cipher.NewCBCEncrypter(block, iv)

	encrypted := make([]byte, len(data))
	mode.CryptBlocks(encrypted, data)

	return encrypted, nil
}

func (c *NIBSSClient) decrypt3DESCBC(data, key []byte) ([]byte, error) {
	block, err := des.NewTripleDESCipher(key)
	if err != nil {
		return nil, err
	}

	if len(data)%block.BlockSize() != 0 {
		return nil, fmt.Errorf("encrypted data is not a multiple of the block size")
	}

	iv := make([]byte, block.BlockSize()) // NIBSS uses a zero IV
	mode := cipher.NewCBCDecrypter(block, iv)

	decrypted := make([]byte, len(data))
	mode.CryptBlocks(decrypted, data)

	unpadded, err := pkcs5Unpad(decrypted, block.BlockSize())
	if err != nil {
		slog.Error("Failed to unpad decrypted data", "error", err, "raw_decrypted", hex.EncodeToString(decrypted))
		// Return raw decrypted data in case of padding error, as it might still be useful for debugging
		return decrypted, fmt.Errorf("failed to unpad decrypted data: %w", err)
	}

	return unpadded, nil
}

func iso9797Pad(data []byte) []byte {
	// ISO 9797-1 Padding Method 2
	padded := append(data, 0x80)
	for len(padded)%8 != 0 {
		padded = append(padded, 0x00)
	}
	return padded
}

func (c *NIBSSClient) computeMAC(data []byte, key []byte) ([]byte, error) {
	block, err := des.NewTripleDESCipher(key)
	if err != nil {
		return nil, err
	}

	paddedData := iso9797Pad(data)

	iv := make([]byte, 8) // Zero IV
	mode := cipher.NewCBCEncrypter(block, iv)

	out := make([]byte, len(paddedData))
	mode.CryptBlocks(out, paddedData)

	// The MAC is the last block of the output
	mac := out[len(out)-8:]
	return mac, nil
}

func (c *NIBSSClient) DialISO8583(ctx context.Context, deadline time.Time) (net.Conn, error) {
	// tlsCert, err := tls.LoadX509KeyPair(c.sslCertPath, c.sslKeyPath)
	// if err != nil {
	// 	return nil, fmt.Errorf("failed to load ISO 8583 TLS cert: %w", err)
	// }
	config := &tls.Config{
		InsecureSkipVerify: true, // trust-all, same as Java
		MinVersion:         tls.VersionTLS12,
		MaxVersion:         tls.VersionTLS12, // pin to TLS 1.2
		CipherSuites: []uint16{
			tls.TLS_ECDHE_RSA_WITH_AES_256_GCM_SHA384,
		},
	}
	dialer := &net.Dialer{Timeout: c.cardSettlementTimeout}
	conn, err := tls.DialWithDialer(dialer, "tcp", c.iso8583BaseURL, config)
	if err != nil {
		return nil, fmt.Errorf("failed to connect to ISO 8583 socket: %w", err)
	}
	_ = conn.SetDeadline(deadline)
	return conn, nil
}

// Deprecated: ProcessCardSettlementWithKeyExchange performs a full 0800/0810 key exchange
// before sending the 0200 settlement. Retained for endpoints that require session key
// negotiation. Use ProcessCardSettlement for the standard NIBSS settlement endpoint.
func (c *NIBSSClient) ProcessCardSettlementWithKeyExchange(ctx context.Context, req0800 *iso8583.Message, isoMsgPayload *iso8583.Message) (*models.CardSettlementResponse, error) {
	body, err := c.circuitBreaker.Execute(func() (any, error) {
		deadline, _ := ctx.Deadline()
		if deadline.IsZero() {
			deadline = time.Now().Add(c.cardSettlementTimeout)
		}

		// 1. Key exchange on its own mTLS connection
		var sessionKey []byte
		{
			keyConn, err := c.DialISO8583(ctx, deadline)
			if err != nil {
				return nil, err
			}
			if err := c.SendISOMessage(keyConn, req0800); err != nil {
				keyConn.Close()
				return nil, err
			}
			resp0810, err := c.ReadISOMessage(keyConn, c.iso8583Service.CreateISO8583_0800_MessageSpec1987())
			keyConn.Close()
			if err != nil {
				return nil, err
			}
			encryptedSessionKey, err := c.extractSessionKeyFrom0810(resp0810)
			if err != nil {
				return nil, err
			}
			sessionKey, err = c.DecryptSessionKey(encryptedSessionKey)
			if err != nil {
				return nil, err
			}
			slog.Debug("Session Key Decrypted and Ready")
		}

		// 2. Encrypt ICC data into field 48 using session key
		iccData, _ := isoMsgPayload.GetBytes(55)
		encryptedPayload, err := c.encrypt3DESCBC(iccData, sessionKey)
		if err != nil {
			return nil, err
		}
		if err := isoMsgPayload.BinaryField(48, encryptedPayload); err != nil {
			return nil, fmt.Errorf("failed to set encrypted payload (field 48): %w", err)
		}

		// 3. Compute and attach MAC (Field 128)
		packedForMAC, err := isoMsgPayload.Pack()
		if err != nil {
			return nil, fmt.Errorf("failed to pack message for MAC computation: %w", err)
		}
		mac, err := c.computeMAC(packedForMAC, sessionKey)
		if err != nil {
			return nil, err
		}
		if err := isoMsgPayload.BinaryField(128, mac); err != nil {
			return nil, fmt.Errorf("failed to attach MAC to field 128: %w", err)
		}

		// 4. Settlement on a fresh mTLS connection
		settleConn, err := c.DialISO8583(ctx, deadline)
		if err != nil {
			return nil, err
		}
		defer settleConn.Close()

		if err := c.SendISOMessage(settleConn, isoMsgPayload); err != nil {
			return nil, err
		}
		resp0210, err := c.ReadISOMessage(settleConn, c.iso8583Service.CreateISO8583MessageSpec1987())
		if err != nil {
			return nil, err
		}
		rc, err := resp0210.GetString(39)
		if err != nil || rc != "00" {
			return nil, fmt.Errorf("settlement failed with response code: %s (err: %v)", rc, err)
		}
		encryptedRespPayload, err := resp0210.GetBytes(48)
		if err != nil {
			return nil, fmt.Errorf("could not get encrypted response payload from field 48: %w", err)
		}
		decrypted, err := c.decrypt3DESCBC(encryptedRespPayload, sessionKey)
		if err != nil {
			return nil, err
		}
		var settlementResp models.CardSettlementResponse
		if err := xml.Unmarshal(decrypted, &settlementResp); err != nil {
			slog.Error("Failed to unmarshal settlement response XML", "body", string(decrypted), "error", err)
			return nil, fmt.Errorf("failed to unmarshal settlement response: %w", err)
		}
		return &settlementResp, nil
	})
	if err != nil {
		return nil, err
	}
	return body.(*models.CardSettlementResponse), nil
}

func (c *NIBSSClient) ProcessCardSettlement(ctx context.Context, isoMsgPayload *iso8583.Message) (*models.CardSettlementResponse, error) {
	body, err := c.circuitBreaker.Execute(func() (any, error) {
		deadline, _ := ctx.Deadline()
		if deadline.IsZero() {
			deadline = time.Now().Add(c.cardSettlementTimeout)
		}

		conn, err := c.DialISO8583(ctx, deadline)
		if err != nil {
			return nil, err
		}
		defer conn.Close()

		if err := c.SendISOMessage(conn, isoMsgPayload); err != nil {
			return nil, err
		}

		resp0210, err := c.ReadISOMessage(conn, c.iso8583Service.CreateISO8583MessageSpec1987())
		if err != nil {
			return nil, err
		}

		rc, err := resp0210.GetString(39)
		if err != nil || rc != "00" {
			return nil, fmt.Errorf("settlement failed with response code: %s (err: %v)", rc, err)
		}

		return &models.CardSettlementResponse{Status: rc, Message: "approved"}, nil
	})

	if err != nil {
		return nil, err
	}

	return body.(*models.CardSettlementResponse), nil
}
