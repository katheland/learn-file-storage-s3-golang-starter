package main

import (
	"net/http"
	"strings"
	"fmt"
	"os"
	"io"
	"mime"
	"time"
	"bytes"
	"context"
	"crypto/rand"
	"encoding/base64"
	"os/exec"
	"encoding/json"
	
	"github.com/bootdotdev/learn-file-storage-s3-golang-starter/internal/database"
	"github.com/google/uuid"
	"github.com/bootdotdev/learn-file-storage-s3-golang-starter/internal/auth"
	"github.com/aws/aws-sdk-go-v2/service/s3"
)

func (cfg *apiConfig) handlerUploadVideo(w http.ResponseWriter, r *http.Request) {
	const maxMemory = 1 << 30

	videoIDString := r.PathValue("videoID")
	videoID, err := uuid.Parse(videoIDString)
	if err != nil {
		respondWithError(w, http.StatusBadRequest, "Invalid ID", err)
		return
	}

	token, err := auth.GetBearerToken(r.Header)
	if err != nil {
		respondWithError(w, http.StatusUnauthorized, "Couldn't find JWT", err)
		return
	}

	userID, err := auth.ValidateJWT(token, cfg.jwtSecret)
	if err != nil {
		respondWithError(w, http.StatusUnauthorized, "Couldn't validate JWT", err)
		return
	}

	fmt.Println("uploading video", videoID, "by user", userID)

	video, err := cfg.db.GetVideo(videoID)
	if err != nil {
		respondWithError(w, http.StatusBadRequest, "Unable to get video metadata", err)
		return
	}
	if video.CreateVideoParams.UserID != userID {
		respondWithError(w, http.StatusUnauthorized, "Unauthorized", err)
		return
	}

	r.ParseMultipartForm(maxMemory)
	file, header, err := r.FormFile("video")
	if err != nil {
		respondWithError(w, http.StatusBadRequest, "Unable to parse video form file", err)
		return
	}
	defer file.Close()

	mediaType, _, err := mime.ParseMediaType(header.Header.Get("Content-Type"))
	if err != nil {
		respondWithError(w, http.StatusBadRequest, "Unable to parse media type", err)
		return
	}
	if mediaType != "video/mp4" {
		respondWithError(w, http.StatusBadRequest, "Should only be an mp4", err)
		return
	}

	tempFile, err := os.CreateTemp("", "tubely-upload.mp4")
	if err != nil {
		respondWithError(w, http.StatusBadRequest, "Error creating temporary video file", err)
		return
	}
	defer os.Remove(tempFile.Name())
	defer tempFile.Close()

	_, err = io.Copy(tempFile, file)
	if err != nil {
		respondWithError(w, http.StatusBadRequest, "Error copying video file", err)
		return
	}

	tempFileProc, err := processVideoForFastStart(tempFile.Name())
	if err != nil {
		respondWithError(w, http.StatusBadRequest, "Error processing fast start", err)
		return
	}
	tempVid, err := os.Open(tempFileProc)
	if err != nil {
		respondWithError(w, http.StatusBadRequest, "Error opening processed fast start", err)
		return
	}
	defer os.Remove(tempVid.Name())
	defer tempVid.Close()
	
	_, err = tempVid.Seek(0, io.SeekStart)
	if err != nil {
		respondWithError(w, http.StatusBadRequest, "Error rewinding temp file", err)
		return
	}

	aspect, err := getVideoAspectRatio(tempVid.Name())
	if err != nil {
		respondWithError(w, http.StatusBadRequest, "Error getting aspect ratio", err)
		return
	}
	var ratio string
	switch aspect {
	case "16:9":
		ratio = "landscape/"
	case "9:16":
		ratio = "portrait/"
	case "other":
		ratio = "other/"
	}
	
	key := make([]byte, 32)
	rand.Read(key)
	videoKey := ratio + base64.RawURLEncoding.EncodeToString(key)

	videoURL := fmt.Sprintf("%v,%v", cfg.s3Bucket, videoKey)

	putObject := s3.PutObjectInput{
		Bucket: &cfg.s3Bucket,
		Key: &videoKey,
		Body: tempVid,
		ContentType: &mediaType,
	}

	_, err = cfg.s3Client.PutObject(context.Background(), &putObject)
	if err != nil {
		respondWithError(w, http.StatusBadRequest, "Error uploading video", err)
		return
	}

	video.VideoURL = &videoURL
	err = cfg.db.UpdateVideo(video)
	if err != nil {
		respondWithError(w, http.StatusBadRequest, "Unable to update video data", err)
		return
	}

	signedVideo, err := cfg.dbVideoToSignedVideo(video)
	if err != nil {
		respondWithError(w, http.StatusBadRequest, "Error getting signed video after upload", err)
		return
	}
	
	respondWithJSON(w, http.StatusOK, signedVideo)
}

type StreamData struct {
	Streams []struct {
		Width int `json:"width"`
		Height int `json:"height"`
	} `json:"streams"`
}

func getVideoAspectRatio(filepath string) (string, error) {
	cmd := exec.Command("ffprobe", "-v", "error", "-print_format", "json", "-show_streams", filepath)
	var buf bytes.Buffer
	cmd.Stdout = &buf
	err := cmd.Run()
	if err != nil {
		return "", err
	}
	var streamData StreamData
	if err = json.Unmarshal(buf.Bytes(), &streamData); err != nil {
		return "", err
	}
	// 16:9 (horizontal)
	if (streamData.Streams[0].Width / 16) == (streamData.Streams[0].Height / 9) {
		return "16:9", nil
	}
	// 9:16 (vertical)
	if (streamData.Streams[0].Width / 9) == (streamData.Streams[0].Height / 16) {
		return "9:16", nil
	}
	// other
	return "other", nil
}

func processVideoForFastStart(filepath string) (string, error) {
	outputPath := filepath + ".processing"
	cmd := exec.Command("ffmpeg", "-i", filepath, "-c", "copy", "-movflags", "faststart", "-f", "mp4", outputPath)
	err := cmd.Run()
	if err != nil {
		return "", err
	}
	return outputPath, nil
}

func generatePresignedURL(s3Client *s3.Client, bucket, key string, expireTime time.Duration) (string, error) {
	presignClient := s3.NewPresignClient(s3Client)
	getObjectInput := s3.GetObjectInput{Bucket: &bucket, Key: &key}
	presignObject, err := presignClient.PresignGetObject(context.Background(), &getObjectInput, s3.WithPresignExpires(expireTime))
	if err != nil {
		return "", err
	}
	return presignObject.URL, nil
}

func (cfg *apiConfig) dbVideoToSignedVideo(video database.Video) (database.Video, error) {
	if video.VideoURL == nil {
		return video, nil
	}

	tokens := strings.Split(*video.VideoURL, ",")
	bucket := tokens[0]
	key := tokens[1]
	
	dur, _ := time.ParseDuration("5s")
	presignedURL, err := generatePresignedURL(cfg.s3Client, bucket, key, dur)
	if err != nil {
		return video, err
	}
	video.VideoURL = &presignedURL
	return video, nil
}