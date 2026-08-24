package main

import (
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
)

// ResolveImage downloads an image to the local cache if needed, then returns the local path.
// Supports file://, http://, and https:// URIs.
func ResolveImage(uri, atlasDirectory string) (string, error) {
	parsedURL, err := url.Parse(uri)
	if err != nil {
		return "", fmt.Errorf("parse uri: %w", err)
	}
	if parsedURL.Scheme == "file" {
		return parsedURL.Path, nil
	}
	if parsedURL.Scheme != "http" && parsedURL.Scheme != "https" {
		return "", fmt.Errorf("unsupported scheme: %s", parsedURL.Scheme)
	}

	cacheDirectory := filepath.Join(atlasDirectory, "images")
	os.MkdirAll(cacheDirectory, 0755)
	cachePath := filepath.Join(cacheDirectory, filepath.Base(parsedURL.Path))

	if _, err := os.Stat(cachePath); err == nil {
		return cachePath, nil
	}

	response, err := http.Get(uri)
	if err != nil {
		return "", fmt.Errorf("download: %w", err)
	}
	defer response.Body.Close()

	if response.StatusCode != http.StatusOK {
		return "", fmt.Errorf("download status: %d", response.StatusCode)
	}

	tempFile, err := os.CreateTemp(cacheDirectory, "img-*")
	if err != nil {
		return "", err
	}
	if _, err := io.Copy(tempFile, response.Body); err != nil {
		tempFile.Close()
		os.Remove(tempFile.Name())
		return "", fmt.Errorf("copy: %w", err)
	}
	tempFile.Close()

	if err := os.Rename(tempFile.Name(), cachePath); err != nil {
		return "", err
	}
	return cachePath, nil
}
