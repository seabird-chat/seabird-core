package main

import (
	"encoding/json"
	"errors"
	"io/ioutil"
	"os"
	"os/signal"
	"path/filepath"
	"strings"
	"syscall"

	"github.com/joho/godotenv"
	"github.com/mattn/go-isatty"
	"github.com/rs/zerolog"

	seabird "github.com/seabird-irc/seabird-core"
)

type tokensConfig struct {
	Tokens map[string][]string
}

// Note that this needs to also reverse the order of the tokens because the
// config has it in the more human readable user : token, but we need it the
// other way around.
func ReadTokenFile(filename string) (map[string]string, error) {
	data, err := ioutil.ReadFile(filename)
	if err != nil {
		return nil, err
	}

	var config tokensConfig
	err = json.Unmarshal(data, &config)
	if err != nil {
		return nil, err
	}

	tokens := make(map[string]string)
	for k, v := range config.Tokens {
		for _, vInner := range v {
			if _, ok := tokens[vInner]; ok {
				return nil, errors.New("duplicate tokens")
			}

			tokens[vInner] = k
		}
	}

	return tokens, nil
}

func EnvDefault(key string, def string) string {
	if ret, ok := os.LookupEnv(key); ok {
		return ret
	}
	return def
}

func Env(logger zerolog.Logger, key string) string {
	ret, ok := os.LookupEnv(key)

	if !ok {
		logger.Fatal().Str("var", key).Msg("Required environment variable not found")
	}

	return ret
}

func main() {
	// Attempt to load from .env if it exists
	_ = godotenv.Load()

	var logger zerolog.Logger

	if isatty.IsTerminal(os.Stdout.Fd()) {
		logger = zerolog.New(zerolog.NewConsoleWriter())
	} else {
		logger = zerolog.New(os.Stdout)
	}

	logger = logger.With().Timestamp().Logger()
	logger.Level(zerolog.InfoLevel)

	tokensFile, err := filepath.Abs(Env(logger, "SEABIRD_TOKEN_FILE"))
	if err != nil {
		logger.Fatal().Err(err).Msg("failed to get absolute path of token file")
	}

	server, err := seabird.NewServer(seabird.ServerConfig{
		BindHost:  EnvDefault("SEABIRD_BIND_HOST", "0.0.0.0:11235"),
		EnableWeb: strings.ToLower(EnvDefault("SEABIRD_ENABLE_WEB", "true")) == "true",
	})
	if err != nil {
		logger.Fatal().Err(err).Msg("failed to create server")
	}

	tokens, err := ReadTokenFile(tokensFile)
	if err != nil {
		logger.Fatal().Err(err).Msg("failed to load tokens")
	}
	server.SetTokens(tokens)

	go func() {
		in := make(chan os.Signal, 5)
		signal.Notify(in, syscall.SIGHUP)

		for range in {
			logger.Info().Msg("got SIGHUP, reloading tokens")

			tokens, err := ReadTokenFile(tokensFile)
			if err != nil {
				logger.Warn().Err(err).Msg("failed to read file")
				continue
			}

			server.SetTokens(tokens)

			logger.Info().Msg("reloaded tokens")
		}
	}()

	err = server.Run()
	if err != nil {
		logger.Fatal().Msgf("failed to start server: %v", err)
	}
}
