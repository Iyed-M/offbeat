package config

import (
	"bytes"
	"os"

	"github.com/pelletier/go-toml/v2"
)

func applyTOMLFromPath(path string, cfg *Config) error {
	data, err := os.ReadFile(path)
	if err != nil {
		return err
	}
	return applyTOML(data, cfg)
}

func applyTOML(data []byte, cfg *Config) error {
	dec := toml.NewDecoder(bytes.NewReader(data))
	dec.DisallowUnknownFields()
	return dec.Decode(cfg)
}

func applyTOMLPermissiveFromPath(path string, cfg any) error {
	data, err := os.ReadFile(path)
	if err != nil {
		return err
	}
	return toml.NewDecoder(bytes.NewReader(data)).Decode(cfg)
}
