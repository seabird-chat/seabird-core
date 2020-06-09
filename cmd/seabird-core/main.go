package main

import (
	"encoding/json"
	"io/ioutil"
	"log"
	"os"
	"path/filepath"
	"strings"

	"github.com/joho/godotenv"
	"github.com/mattn/go-isatty"
	"github.com/rs/zerolog"
	"gopkg.in/fsnotify.v1"

	seabird "github.com/seabird-irc/seabird-core"
)

func ReadTokenFile(filename string) (map[string]string, error) {
	data, err := ioutil.ReadFile(filename)
	if err != nil {
		return nil, err
	}

	var config seabird.ServerConfig
	err = json.Unmarshal(data, &config)
	if err != nil {
		return nil, err
	}

	return config.Tokens, nil
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

	tokensDir := filepath.Dir(tokensFile)
	tokensFilename := filepath.Base(tokensFile)

	server, err := seabird.NewServer(seabird.ServerConfig{
		BindHost:  EnvDefault("SEABIRD_BIND_HOST", ":11235"),
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

	watcher, err := fsnotify.NewWatcher()
	if err != nil {
		log.Fatal(err)
	}
	defer watcher.Close()

	err = watcher.Add(tokensDir)
	if err != nil {
		logger.Fatal().Err(err).Msg("failed to watch tokens dir")
	}

	go func() {
		for {
			select {
			case event, ok := <-watcher.Events:
				if !ok {
					logger.Fatal().Msg("fswatcher exited early")
				}

				logger.Debug().Msgf("fswatch event: %v", event.Name)

				if filepath.Base(event.Name) != tokensFilename {
					logger.Debug().Msgf(
						"Skipping file event because %q != %q",
						filepath.Base(event.Name), tokensFilename,
					)
					continue
				}

				if event.Op&fsnotify.Write != fsnotify.Write && event.Op&fsnotify.Create != fsnotify.Create {
					logger.Debug().Msg("Skipping file event because event was not Write or Create")
					continue
				}

				logger.Info().Msgf("tokens file modified: %s", event.Name)

				tokens, err := ReadTokenFile(tokensFile)
				if err != nil {
					logger.Warn().Err(err).Msg("failed to read file")
					continue
				}
				server.SetTokens(tokens)
			case err, ok := <-watcher.Errors:
				if !ok {
					logger.Fatal().Msg("fswatcher exited early")
				}

				logger.Error().Err(err).Msg("failed to read file")
			}
		}
	}()

	err = server.Run()
	if err != nil {
		logger.Fatal().Msgf("failed to start server: %v", err)
	}
}
