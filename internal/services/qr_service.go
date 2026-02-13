package services

import (
	"bytes"
	"context"
	"crypto/rand"
	"database/sql"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"image"
	"image/color"
	"image/draw"
	"image/png"
	"log"
	"os"
	"time"

	"github.com/go-redis/redis/v8"
	"github.com/skip2/go-qrcode"
	"github.com/spf13/viper"
	"github.com/srwiley/oksvg"
	"github.com/srwiley/rasterx"
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

func (s *QRService) GenerateQRCode(ctx context.Context, userID string) (string, string, error) {
	nonce := s.generateNonce()
	expiryMinutes := viper.GetInt("QR_EXPIRY_MINUTES")
	if expiryMinutes == 0 {
		expiryMinutes = 5
	}
	expiryDuration := time.Duration(expiryMinutes) * time.Minute
	expiresAt := time.Now().Add(expiryDuration).Unix()
	qrData := map[string]any{
		"userId":    userID,
		"timestamp": time.Now().Unix(),
		"expiresAt": expiresAt,
		"nonce":     nonce,
	}

	jsonData, err := json.Marshal(qrData)
	if err != nil {
		return "", "", err
	}

	qrToken := base64.URLEncoding.EncodeToString(jsonData)
	key := fmt.Sprintf("qr:%s", qrToken)
	if err := s.redis.Set(ctx, key, jsonData, expiryDuration).Err(); err != nil {
		return "", "", err
	}

	appScheme := viper.GetString("app.scheme")
	if appScheme == "" {
		appScheme = "ruralpay"
	}
	appRoute := viper.GetString("app.qr_route")
	if appRoute == "" {
		appRoute = "pay/qr"
	}
	appDomain := viper.GetString("app.domain")
	if appDomain == "" {
		appDomain = "app.ruralpay.com"
	}
	deepLink := fmt.Sprintf("https://%s/%s?token=%s", appDomain, appRoute, qrToken)
	log.Println(deepLink)
	qr, err := qrcode.New(deepLink, qrcode.Highest)
	if err != nil {
		return "", "", err
	}

	qrImg := qr.Image(1024)
	qrWithLogo, err := s.addLogoToQR(qrImg)
	if err != nil {
		return "", "", err
	}

	var buf bytes.Buffer
	if err := png.Encode(&buf, qrWithLogo); err != nil {
		return "", "", err
	}

	qrImage := base64.StdEncoding.EncodeToString(buf.Bytes())
	return deepLink, qrImage, nil
}

func (s *QRService) addLogoToQR(qrImg image.Image) (image.Image, error) {
	logoPath := "./static/bank-logos/demo.svg"
	svgData, err := os.ReadFile(logoPath)
	if err != nil {
		return qrImg, nil
	}

	icon, err := oksvg.ReadIconStream(bytes.NewReader(svgData))
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
	icon.SetTarget(0, 0, float64(logoSize), float64(logoSize))
	scanner := rasterx.NewDasher(logoSize, logoSize, rasterx.NewScannerGV(logoSize, logoSize, logoImg, logoImg.Bounds()))
	icon.Draw(scanner, 1.0)

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
	expiryMinutes := viper.GetInt("QR_EXPIRY_MINUTES")
	if expiryMinutes == 0 {
		expiryMinutes = 5
	}
	key := fmt.Sprintf("qr:%s", qrData)

	data, err := s.redis.Get(ctx, key).Bytes()
	if err == redis.Nil {
		return nil, fmt.Errorf("invalid or expired QR code")
	}
	if err != nil {
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

	return result, nil
}

func (s *QRService) generateNonce() string {
	b := make([]byte, 16)
	rand.Read(b)
	return base64.URLEncoding.EncodeToString(b)
}
