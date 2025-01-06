package config

import "os"

type MatrixOptions struct {
	GRPCClientOptions GRPCClientOptions
	HomeServerURL     string
	AccessToken       string
	UserID            string
}

func homeServerUrl() string {
	return os.Getenv("MATRIX_HOMESERVER_URL")
}

func accessToken() string {
	return os.Getenv("MATRIX_ACCESS_TOKEN")
}

func userId() string {
	return os.Getenv("MATRIX_USER_ID")
}
