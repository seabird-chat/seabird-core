package main

import (
	"github.com/sirupsen/logrus"

	seabird "github.com/belak/seabird-core"
)

func main() {
	logrus.SetLevel(logrus.InfoLevel)

	server, err := seabird.NewServer()
	if err != nil {
		logrus.Fatalf("failed to create server: %v", err)
	}

	err = server.ListenAndServe()
	if err != nil {
		logrus.Fatalf("failed to start server: %v", err)
	}
}
