package main

import (
	"fmt"
	"net/http"
	"io"
	"os"
	"mime"
	"path/filepath"
	"crypto/rand"
	"encoding/base64"

	"github.com/bootdotdev/learn-file-storage-s3-golang-starter/internal/auth"
	"github.com/google/uuid"
)

func (cfg *apiConfig) handlerUploadThumbnail(w http.ResponseWriter, r *http.Request) {
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


	fmt.Println("uploading thumbnail for video", videoID, "by user", userID)

	// implement the upload here
	const maxMemory = 10 << 20
	r.ParseMultipartForm(maxMemory)
	file, header, err := r.FormFile("thumbnail")
	if err != nil {
		respondWithError(w, http.StatusBadRequest, "Unable to parse thumbnail form file", err)
		return
	}
	defer file.Close()

	mediaType, _, err := mime.ParseMediaType(header.Header.Get("Content-Type"))
	if err != nil {
		respondWithError(w, http.StatusBadRequest, "Unable to parse media type", err)
		return
	}
	if mediaType != "image/jpeg" && mediaType != "image/png" {
		respondWithError(w, http.StatusBadRequest, "Should only be a jpeg or a png", err)
		return
	}

	video, err := cfg.db.GetVideo(videoID)
	if err != nil {
		respondWithError(w, http.StatusBadRequest, "Unable to get video metadata", err)
		return
	}
	if video.CreateVideoParams.UserID != userID {
		respondWithError(w, http.StatusUnauthorized, "Unauthorized", err)
		return
	}

	fileExtension, err := mime.ExtensionsByType(mediaType)
	if err != nil {
		respondWithError(w, http.StatusBadRequest, "The file extension messed up", err)
		return
	}

	key := make([]byte, 32)
	rand.Read(key)
	videoURL := base64.RawURLEncoding.EncodeToString(key)
	fileURL := filepath.Join(cfg.assetsRoot, fmt.Sprintf("%v%v", videoURL, fileExtension[0]))
	fmt.Println(fileURL)
	imgFile, err := os.Create(fileURL)
	if err != nil {
		respondWithError(w, http.StatusBadRequest, "Error making the thumbnail file", err)
		return
	}
	_, err = io.Copy(imgFile, file)
	if err != nil {
		respondWithError(w, http.StatusBadRequest, "Error copying the thumbnail file", err)
		return
	}
	
	thumbURL := fmt.Sprintf("http://localhost:%v/assets/%v%v", cfg.port, videoURL, fileExtension[0])

	video.ThumbnailURL = &thumbURL
	err = cfg.db.UpdateVideo(video)
	if err != nil {
		respondWithError(w, http.StatusBadRequest, "Unable to update video data", err)
		return
	}

	respondWithJSON(w, http.StatusOK, video)
}
