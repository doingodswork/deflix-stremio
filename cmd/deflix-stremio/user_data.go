package main

import (
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	"go.uber.org/zap"
)

type userData struct {
	// RealDebrid
	RDtoken  string `json:"rdToken,omitempty"`
	RDoauth2 string `json:"rdOAUTH2,omitempty"`
	RDremote bool   `json:"rdRemote,omitempty"`
	// AllDebrid
	ADkey string `json:"adKey,omitempty"`
	// Premiumize
	PMkey    string `json:"pmKey,omitempty"`
	PMoauth2 string `json:"pmOAUTH2,omitempty"`
}

func (ud userData) encode(logger *zap.Logger) (string, error) {
	logger.Debug("Encoding user data")
	userDataJSON, err := json.Marshal(ud)
	if err != nil {
		return "", err
	}
	userDataEncoded := base64.RawURLEncoding.EncodeToString(userDataJSON)
	return userDataEncoded, nil
}

func decodeUserData(data string, logger *zap.Logger) (userData, error) {
	logger.Debug("Decoding user data", zap.String("userData", data))

	// Legacy user data (plain string, RD only).
	// - If it's ending with "-remote" it's 100% clear
	// - RD API tokens always seem to be 52 chars long
	// - Base64 encoded JSON starts with "eyJ" or "eyI"
	if strings.HasSuffix(data, "-remote") {
		tokenParts := strings.Split(data, "-")
		if len(tokenParts) > 2 {
			return userData{}, errors.New("legacy userData was not correctly formatted")
		}
		logger.Info("A legacy API token is being used", zap.Bool("remote", true))
		return userData{
			RDtoken:  tokenParts[0],
			RDremote: true,
		}, nil
	} else if len(data) == 52 && !strings.HasPrefix(data, "eyJ") && !strings.HasPrefix(data, "eyI") {
		logger.Info("A legacy API token is being used", zap.Bool("remote", false))
		return userData{
			RDtoken:  data,
			RDremote: false,
		}, nil
	}

	// If there's padding, we remove it, so that the decoding works with both:
	data = strings.TrimSuffix(data, "=")
	var userDataDecoded []byte
	userDataDecoded, err := base64.RawURLEncoding.DecodeString(data)
	if err != nil {
		// We use WARN instead of ERROR because it's most likely an *encoding* error on the client side
		logger.Warn("Couldn't decode user data", zap.Error(err))
		return userData{}, err
	}

	ud := userData{}
	if err := json.Unmarshal(userDataDecoded, &ud); err != nil {
		logger.Warn("Couldn't unmarshal user data", zap.Error(err))
		return userData{}, err
	}
	logger.Debug("Decoded user data", zap.String("userData", fmt.Sprintf("%+v", ud)))
	return ud, nil
}
