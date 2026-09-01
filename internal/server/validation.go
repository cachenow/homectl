package server

import (
	"errors"
	"strings"
	"unicode"
	"unicode/utf8"
)

func normalizeUsername(value string) (string, error) {
	value = strings.TrimSpace(value)
	if !utf8.ValidString(value) {
		return "", errors.New("username must be valid UTF-8")
	}
	count := utf8.RuneCountInString(value)
	if count < 1 || count > 64 {
		return "", errors.New("username must be 1-64 characters")
	}
	for _, r := range value {
		if unicode.IsControl(r) {
			return "", errors.New("username must not contain control characters")
		}
	}
	return value, nil
}

func normalizeDeviceName(value string) (string, error) {
	value = strings.TrimSpace(value)
	if !utf8.ValidString(value) {
		return "", errors.New("device name must be valid UTF-8")
	}
	count := utf8.RuneCountInString(value)
	if count < 1 || count > 128 {
		return "", errors.New("device name must be 1-128 characters")
	}
	for _, r := range value {
		if unicode.IsControl(r) {
			return "", errors.New("device name must not contain control characters")
		}
	}
	return value, nil
}
