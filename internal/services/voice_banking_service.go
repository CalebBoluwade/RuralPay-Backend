package services

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"strings"
	"time"

	speech "cloud.google.com/go/speech/apiv1"
	"cloud.google.com/go/speech/apiv1/speechpb"
	"github.com/ruralpay/backend/internal/utils"
)

type VoiceBankingService struct {
	client *speech.Client
}

type TranscribeRequest struct {
	Audio        string `json:"audio" validate:"required"`
	Encoding     string `json:"encoding"`
	SampleRate   int    `json:"sample_rate"`
	LanguageCode string `json:"language_code"`
}

type TranscribeResponse struct {
	Transcript string  `json:"transcript"`
	Confidence float32 `json:"confidence"`
	Duration   float64 `json:"duration_seconds"`
}

func NewVoiceBankingService() *VoiceBankingService {
	ctx := context.Background()
	client, err := speech.NewClient(ctx)
	if err != nil {
		slog.Error("Warning: Failed to initialize speech client: %v", "error", err)
		return &VoiceBankingService{client: nil}
	}
	return &VoiceBankingService{client: client}
}

func (s *VoiceBankingService) TranscribeAudio(w http.ResponseWriter, r *http.Request) {
	userID, ok := r.Context().Value("userID").(string)
	if !ok || userID == "" {
		utils.SendErrorResponse(w, utils.UnauthorizedError, http.StatusUnauthorized, nil)
		return
	}

	maxBytes := 10 * 1024 * 1024
	r.Body = http.MaxBytesReader(w, r.Body, int64(maxBytes))

	dec := json.NewDecoder(r.Body)
	dec.DisallowUnknownFields()

	var req TranscribeRequest
	if err := dec.Decode(&req); err != nil {
		utils.SendErrorResponse(w, utils.InvalidRequestError, http.StatusBadRequest, nil)
		return
	}

	if err := dec.Decode(&struct{}{}); err != io.EOF {
		utils.SendErrorResponse(w, utils.SingleObjectError, http.StatusBadRequest, nil)
		return
	}

	if req.Audio == "" {
		utils.SendErrorResponse(w, "Audio is required", http.StatusBadRequest, nil)
		return
	}

	if req.Encoding == "" {
		req.Encoding = "LINEAR16"
	}
	if req.SampleRate == 0 {
		req.SampleRate = 16000
	}
	if req.LanguageCode == "" {
		req.LanguageCode = "en-US"
	}

	startTime := time.Now()
	transcript, confidence, err := s.Transcribe(r.Context(), req)
	duration := time.Since(startTime).Seconds()

	if err != nil {
		slog.Error("[VOICE] Audio Transcription Failed for user %s: %v", "user_id", userID, "error", err)
		utils.SendErrorResponse(w, utils.ProcessingFailed, http.StatusFailedDependency, nil)
		return
	}

	slog.Info("[VOICE] Transcription successful for user %s, confidence: %.2f", "user_id", userID, "confidence", confidence)
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(TranscribeResponse{
		Transcript: transcript,
		Confidence: confidence,
		Duration:   duration,
	})
}

func (s *VoiceBankingService) Transcribe(ctx context.Context, req TranscribeRequest) (string, float32, error) {
	if s.client == nil {
		return s.mockTranscribe(req)
	}

	audioBytes, err := base64.StdEncoding.DecodeString(req.Audio)
	if err != nil {
		return "", 0, fmt.Errorf("failed to decode audio: %w", err)
	}

	if len(audioBytes) == 0 {
		return "", 0, errors.New("audio data is empty")
	}

	encoding, err := parseEncoding(req.Encoding)
	if err != nil {
		return "", 0, err
	}

	speechReq := &speechpb.RecognizeRequest{
		Config: &speechpb.RecognitionConfig{
			Encoding:                   encoding,
			SampleRateHertz:            int32(req.SampleRate),
			LanguageCode:               req.LanguageCode,
			EnableAutomaticPunctuation: true,
			Model:                      "latest_long",
			UseEnhanced:                true,
		},
		Audio: &speechpb.RecognitionAudio{
			AudioSource: &speechpb.RecognitionAudio_Content{
				Content: audioBytes,
			},
		},
	}

	timeoutCtx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()

	resp, err := s.client.Recognize(timeoutCtx, speechReq)
	if err != nil {
		return "", 0, fmt.Errorf("recognition failed: %w", err)
	}

	if len(resp.Results) == 0 {
		return "", 0, errors.New("no transcription results")
	}

	var transcript strings.Builder
	var totalConfidence float32
	var count int

	for _, result := range resp.Results {
		if len(result.Alternatives) > 0 {
			alternative := result.Alternatives[0]
			transcript.WriteString(alternative.Transcript)
			transcript.WriteString(" ")
			totalConfidence += alternative.Confidence
			count++
		}
	}

	if count == 0 {
		return "", 0, errors.New("no alternatives in results")
	}

	avgConfidence := totalConfidence / float32(count)
	finalTranscript := strings.TrimSpace(transcript.String())
	return finalTranscript, avgConfidence, nil
}

func parseEncoding(encoding string) (speechpb.RecognitionConfig_AudioEncoding, error) {
	switch strings.ToUpper(encoding) {
	case "LINEAR16":
		return speechpb.RecognitionConfig_LINEAR16, nil
	case "FLAC":
		return speechpb.RecognitionConfig_FLAC, nil
	case "MULAW":
		return speechpb.RecognitionConfig_MULAW, nil
	case "AMR":
		return speechpb.RecognitionConfig_AMR, nil
	case "AMR_WB":
		return speechpb.RecognitionConfig_AMR_WB, nil
	case "OGG_OPUS":
		return speechpb.RecognitionConfig_OGG_OPUS, nil
	case "SPEEX_WITH_HEADER_BYTE":
		return speechpb.RecognitionConfig_SPEEX_WITH_HEADER_BYTE, nil
	case "WEBM_OPUS":
		return speechpb.RecognitionConfig_WEBM_OPUS, nil
	default:
		return speechpb.RecognitionConfig_ENCODING_UNSPECIFIED, fmt.Errorf("unsupported encoding: %s", encoding)
	}
}

func (s *VoiceBankingService) mockTranscribe(req TranscribeRequest) (string, float32, error) {
	audioBytes, err := base64.StdEncoding.DecodeString(req.Audio)
	if err != nil {
		return "", 0, fmt.Errorf("failed to decode audio: %w", err)
	}

	if len(audioBytes) == 0 {
		return "", 0, errors.New("audio data is empty")
	}

	return "Mock transcription: Check my balance", 0.95, nil
}

func (s *VoiceBankingService) Close() error {
	if s.client != nil {
		return s.client.Close()
	}
	return nil
}
