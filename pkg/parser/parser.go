package parser

import (
	"encoding/json"
	"errors"
	"math"
	"strings"
)

var (
	ErrTopicLengthTooShort = errors.New("topic length too short")
	ErrPayloadNotString    = errors.New("payload cannot be marshalled to string")
)

type payloadValue struct {
	Value interface{} `json:"value"`
}

func parse(payload []byte) (interface{}, error) {
	v := payloadValue{}
	if err := json.Unmarshal(payload, &v); err != nil {
		return "", err
	}

	return v.Value, nil
}

func ParsePayloadAsString(payload []byte) (string, error) {
	v, err := parse(payload)
	if err != nil {
		return "", nil
	}

	val, ok := v.(string)
	if !ok {
		return "", ErrPayloadNotString
	}

	return val, nil
}

type Component struct {
	ComponentType string
	ComponentID   string
	ComponentPath string
	Value         interface{}
}

func ParseComponent(topic string, payload []byte) (*Component, error) {
	topicParts := strings.Split(topic, "/")

	if len(topicParts) < 5 {
		return nil, ErrTopicLengthTooShort
	}

	value, err := parse(payload)
	if err != nil {
		return nil, err
	}

	return &Component{
		ComponentType: topicParts[2],
		ComponentID:   topicParts[3],
		ComponentPath: strings.Join(topicParts[4:], "/"),
		Value:         value,
	}, nil
}

func (c *Component) AsMetric() float64 {
	v, ok := c.Value.(float64)
	if !ok {
		math.NaN()
	}
	return v
}
