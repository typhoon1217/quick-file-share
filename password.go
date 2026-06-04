package main

import (
	"strings"

	"golang.org/x/crypto/bcrypt"
)

func hashItemPassword(raw string) (string, error) {
	password := strings.TrimSpace(raw)
	if password == "" {
		return "", nil
	}
	hash, err := bcrypt.GenerateFromPassword([]byte(password), bcrypt.DefaultCost)
	if err != nil {
		return "", err
	}
	return string(hash), nil
}

func itemPasswordMatches(hash, raw string) bool {
	if strings.TrimSpace(hash) == "" {
		return true
	}
	return bcrypt.CompareHashAndPassword([]byte(hash), []byte(strings.TrimSpace(raw))) == nil
}
