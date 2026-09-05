package config

import (
	"io"

	"gopkg.in/yaml.v3"
)

func newYAMLDecoder(reader io.Reader) *yaml.Decoder {
	decoder := yaml.NewDecoder(reader)
	decoder.KnownFields(true)
	return decoder
}
