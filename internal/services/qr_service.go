package services

import (
	"bytes"
	"context"
	"crypto/rand"
	"database/sql"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"image"
	"image/color"
	"image/draw"
	"image/png"
	"log/slog"
	"os"
	"time"

	"github.com/go-redis/redis/v8"
	"github.com/skip2/go-qrcode"
	"github.com/spf13/viper"
)

type QRService struct {
	db    *sql.DB
	redis *redis.Client
}

func NewQRService(db *sql.DB, redis *redis.Client) *QRService {
	return &QRService{
		db:    db,
		redis: redis,
	}
}

func (s *QRService) GenerateQRCode(ctx context.Context, userID string, merchantID string) (string, string, error) {
	slog.Info("qr.generate.start")
	if merchantID == "" {
		slog.Error("qr.generate.error", "merchant", "Invalid Merchant")
		return "", "", errors.New("invalid Merchant")
	}

	nonce := s.generateNonce()
	expiryMinutes := viper.GetInt("QR_EXPIRY_MINUTES")
	if expiryMinutes == 0 {
		expiryMinutes = 5
	}
	expiryDuration := time.Duration(expiryMinutes) * time.Minute
	expiresAt := time.Now().Add(expiryDuration).Unix()
	qrData := map[string]any{
		"userId":     userID,
		"merchantId": merchantID,
		"timestamp":  time.Now().Unix(),
		"expiresAt":  expiresAt,
		"nonce":      nonce,
	}

	jsonData, err := json.Marshal(qrData)
	if err != nil {
		slog.Error("qr.generate.error", "error", err)
		return "", "", err
	}

	qrToken := base64.URLEncoding.EncodeToString(jsonData)
	key := fmt.Sprintf("qr:%s", qrToken)
	if err := s.redis.Set(ctx, key, jsonData, expiryDuration).Err(); err != nil {
		slog.Error("qr.generate.error", "error", err)
		return "", "", err
	}

	emvData := s.buildEMVCoQR(qrToken)
	slog.Info("qr.generate.emvco_built")
	qr, err := qrcode.New(emvData, qrcode.Highest)
	if err != nil {
		slog.Error("qr.generate.error", "error", err)
		return "", "", err
	}

	qrImg := qr.Image(1024)
	qrWithLogo, err := s.addLogoToQR(qrImg)
	if err != nil {
		slog.Error("qr.generate.error", "error", err)
		return "", "", err
	}

	var buf bytes.Buffer
	if err := png.Encode(&buf, qrWithLogo); err != nil {
		slog.Error("qr.generate.error", "error", err)
		return "", "", err
	}

	qrImage := base64.StdEncoding.EncodeToString(buf.Bytes())
	return emvData, qrImage, nil
}

func (s *QRService) addLogoToQR(qrImg image.Image) (image.Image, error) {
	logoPath := "./static/bank-logos/nibss.png"
	file, err := os.Open(logoPath)
	if err != nil {
		return qrImg, nil
	}
	defer file.Close()

	logo, err := png.Decode(file)
	if err != nil {
		return qrImg, nil
	}

	logoSize := qrImg.Bounds().Dx() / 7
	bgSize := logoSize + 16
	limeGreen := color.RGBA{50, 205, 50, 255}
	radius := float64(bgSize) / 2
	center := float64(bgSize) / 2

	roundedBg := image.NewRGBA(image.Rect(0, 0, bgSize, bgSize))
	for y := 0; y < bgSize; y++ {
		for x := 0; x < bgSize; x++ {
			dx := float64(x) - center
			dy := float64(y) - center
			if dx*dx+dy*dy <= radius*radius {
				roundedBg.Set(x, y, limeGreen)
			}
		}
	}

	logoImg := image.NewRGBA(image.Rect(0, 0, logoSize, logoSize))
	logoBounds := logo.Bounds()
	for y := 0; y < logoSize; y++ {
		for x := 0; x < logoSize; x++ {
			srcX := logoBounds.Min.X + (x * logoBounds.Dx() / logoSize)
			srcY := logoBounds.Min.Y + (y * logoBounds.Dy() / logoSize)
			logoImg.Set(x, y, logo.At(srcX, srcY))
		}
	}

	logoRadius := float64(logoSize) / 2
	logoCenter := float64(logoSize) / 2
	roundedLogo := image.NewRGBA(image.Rect(0, 0, logoSize, logoSize))
	for y := 0; y < logoSize; y++ {
		for x := 0; x < logoSize; x++ {
			dx := float64(x) - logoCenter
			dy := float64(y) - logoCenter
			if dx*dx+dy*dy <= logoRadius*logoRadius {
				roundedLogo.Set(x, y, logoImg.At(x, y))
			}
		}
	}

	draw.Draw(roundedBg, roundedLogo.Bounds().Add(image.Pt(8, 8)), roundedLogo, image.Point{}, draw.Over)

	result := image.NewRGBA(qrImg.Bounds())
	draw.Draw(result, qrImg.Bounds(), qrImg, image.Point{}, draw.Src)

	offset := image.Pt(
		(qrImg.Bounds().Dx()-roundedBg.Bounds().Dx())/2,
		(qrImg.Bounds().Dy()-roundedBg.Bounds().Dy())/2,
	)
	draw.Draw(result, roundedBg.Bounds().Add(offset), roundedBg, image.Point{}, draw.Over)

	return result, nil
}

func (s *QRService) ProcessQRCode(ctx context.Context, qrData string) (map[string]any, error) {
	token := qrData
	if len(qrData) > 100 {
		if qrData[:6] == "000201" {
			parsedToken, err := s.parseEMVCoQR(qrData)
			if err != nil {
				slog.Error("qr.process.emvco_parse_failed", "error", err)
				return nil, fmt.Errorf("invalid EMVCo QR format: %v", err)
			}
			token = parsedToken
		} else {
			lastEqualIdx := -1
			for i := len(qrData) - 1; i >= 0; i-- {
				if qrData[i] == '=' {
					lastEqualIdx = i
					break
				}
			}
			if lastEqualIdx > 0 && lastEqualIdx+1 < len(qrData) {
				if qrData[lastEqualIdx+1] >= '0' && qrData[lastEqualIdx+1] <= '9' {
					token = qrData[:lastEqualIdx+2]
				}
			}
		}
	}

	expiryMinutes := viper.GetInt("QR_EXPIRY_MINUTES")
	if expiryMinutes == 0 {
		expiryMinutes = 5
	}
	key := fmt.Sprintf("qr:%s", token)

	data, err := s.redis.Get(ctx, key).Bytes()
	if err == redis.Nil {
		slog.Warn("qr.process.token_not_found")
		return nil, fmt.Errorf("Invalid OR Expired QR code")
	}
	if err != nil {
		slog.Error("qr.process.redis_error", "error", err)
		return nil, err
	}

	var result map[string]any
	if err := json.Unmarshal(data, &result); err != nil {
		return nil, err
	}

	if expiresAt, ok := result["expiresAt"].(float64); ok {
		if time.Now().Unix() > int64(expiresAt) {
			s.redis.Del(ctx, key)
			return nil, fmt.Errorf("QR code has expired")
		}
	}

	nonce, ok := result["nonce"].(string)
	if !ok {
		return nil, fmt.Errorf("invalid QR code format")
	}

	replayKey := fmt.Sprintf("qr:used:%s", nonce)
	exists, err := s.redis.Exists(ctx, replayKey).Result()
	if err != nil {
		return nil, err
	}
	if exists > 0 {
		return nil, fmt.Errorf("QR code already used")
	}

	replayTTL := time.Duration(expiryMinutes*2) * time.Minute
	if err := s.redis.Set(ctx, replayKey, "1", replayTTL).Err(); err != nil {
		return nil, err
	}

	s.redis.Del(ctx, key)

	slog.Info("qr.process.success")
	return result, nil
}

func (s *QRService) generateNonce() string {
	b := make([]byte, 16)
	rand.Read(b)
	return base64.URLEncoding.EncodeToString(b)
}

func (s *QRService) buildEMVCoQR(token string) string {
	payloadFormatIndicator := "000201"
	pointOfInitiation := "010212"

	appDomain := viper.GetString("app.domain")
	if appDomain == "" {
		appDomain = "app.ruralpay.com"
	}
	appRoute := viper.GetString("app.qr_route")
	if appRoute == "" {
		appRoute = "pay/qr"
	}
	deepLink := fmt.Sprintf("https://%s/%s?token=%s", appDomain, appRoute, token)

	merchantID := viper.GetString("emvco.merchant_id")
	if merchantID == "" {
		merchantID = "RURALPAY"
	}

	merchantAccountInfo := fmt.Sprintf("26%02d00%02d%s01%02d%s",
		len(fmt.Sprintf("00%02d%s01%02d%s", len(merchantID), merchantID, len(deepLink), deepLink)),
		len(merchantID), merchantID,
		len(deepLink), deepLink)

	transactionCurrency := "5303566"
	countryCode := "5802NG"

	merchantName := viper.GetString("emvco.merchant_name")
	if merchantName == "" {
		merchantName = "RuralPay"
	}
	merchantNameField := fmt.Sprintf("59%02d%s", len(merchantName), merchantName)

	merchantCity := viper.GetString("emvco.merchant_city")
	if merchantCity == "" {
		merchantCity = "Lagos"
	}
	merchantCityField := fmt.Sprintf("60%02d%s", len(merchantCity), merchantCity)

	payload := payloadFormatIndicator + pointOfInitiation + merchantAccountInfo + transactionCurrency + countryCode + merchantNameField + merchantCityField

	crc := s.calculateCRC16(payload + "6304")
	return payload + fmt.Sprintf("6304%04X", crc)
}

func (s *QRService) calculateCRC16(data string) uint16 {
	var crc uint16 = 0xFFFF
	for i := 0; i < len(data); i++ {
		crc ^= uint16(data[i]) << 8
		for j := 0; j < 8; j++ {
			if crc&0x8000 != 0 {
				crc = (crc << 1) ^ 0x1021
			} else {
				crc <<= 1
			}
		}
	}
	return crc & 0xFFFF
}

func (s *QRService) parseEMVCoQR(qrData string) (string, error) {
	if len(qrData) < 10 {
		return "", fmt.Errorf("QR data too short")
	}

	i := 0
	for i < len(qrData)-4 {
		if i+4 > len(qrData) {
			break
		}
		tag := qrData[i : i+2]
		lengthStr := qrData[i+2 : i+4]
		length := 0
		fmt.Sscanf(lengthStr, "%02d", &length)

		if i+4+length > len(qrData) {
			break
		}
		value := qrData[i+4 : i+4+length]

		if tag == "26" {
			token, err := s.parseTag26(value)
			if err == nil {
				return token, nil
			}
		}

		i += 4 + length
	}

	return "", fmt.Errorf("token not found in QR data")
}

func (s *QRService) parseTag26(data string) (string, error) {
	i := 0
	for i < len(data) {
		if i+4 > len(data) {
			break
		}
		tag := data[i : i+2]
		lengthStr := data[i+2 : i+4]
		length := 0
		fmt.Sscanf(lengthStr, "%02d", &length)

		if i+4+length > len(data) {
			break
		}
		value := data[i+4 : i+4+length]

		if tag == "01" {
			if len(value) > 8 && value[:8] == "https://" {
				tokenStart := -1
				for j := 0; j < len(value); j++ {
					if j+6 < len(value) && value[j:j+7] == "token=" {
						tokenStart = j + 7
						break
					}
				}
				if tokenStart > 0 && tokenStart < len(value) {
					return value[tokenStart:], nil
				}
			}
		}

		i += 4 + length
	}

	return "", fmt.Errorf("URL not found in tag 26")
}
