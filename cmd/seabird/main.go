package main

import (
	"os"

	"github.com/sirupsen/logrus"

	seabird "github.com/belak/seabird-core"
)

func EnvDefault(key string, def string) string {
	if ret, ok := os.LookupEnv(key); ok {
		return ret
	}
	return def
}

func Env(key string) string {
	ret, ok := os.LookupEnv(key)

	if !ok {
		logrus.WithField("var", key).Fatal("Required environment variable not found")
	}

	return ret
}

func main() {
	logrus.SetLevel(logrus.InfoLevel)

	nick := Env("SEABIRD_NICK")
	user := EnvDefault("SEABIRD_USER", nick)
	name := EnvDefault("SEABIRD_NAME", user)

	server, err := seabird.NewServer(seabird.ServerConfig{
		IrcHost:       Env("SEABIRD_IRC_HOST"),
		CommandPrefix: EnvDefault("SEABIRD_COMMAND_PREFIX", "!"),
		BindHost:      EnvDefault("SEABIRD_BIND_HOST", ":11235"),
		Nick:          nick,
		User:          user,
		Name:          name,
		Pass:          os.Getenv("SEABIRD_PASS"),
	})
	if err != nil {
		logrus.Fatalf("failed to create server: %v", err)
	}

	err = server.ListenAndServe()
	if err != nil {
		logrus.Fatalf("failed to start server: %v", err)
	}
}
