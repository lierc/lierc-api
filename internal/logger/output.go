package logger

import (
	"encoding/json"
	"log"

	"github.com/nsqio/go-nsq"
)

// publisher is the part of an nsq.Producer NSQOutput uses.
type publisher interface {
	Publish(topic string, body []byte) error
}

// NSQOutput publishes each message, as JSON, to the NSQ topic "logged",
// which the API's stream server consumes.
type NSQOutput struct {
	pub      publisher
	producer *nsq.Producer
}

// NewNSQOutput publishes on nsqd at config's NSQDHost. The producer
// connects when it first publishes.
func NewNSQOutput(config *Config) (*NSQOutput, error) {
	producer, err := nsq.NewProducer(config.nsqd(), nsq.NewConfig())
	if err != nil {
		return nil, err
	}
	return &NSQOutput{pub: producer, producer: producer}, nil
}

// Logged publishes e; failures are logged.
func (o *NSQOutput) Logged(e *LoggedMessage) {
	body, err := json.Marshal(e)
	if err != nil {
		log.Printf("encoding logged message: %v", err)
		return
	}
	if err := o.pub.Publish("logged", body); err != nil {
		log.Printf("publishing to logged: %v", err)
	}
}

// Stop disconnects from nsqd, after the logger's Stop.
func (o *NSQOutput) Stop() {
	if o.producer != nil {
		o.producer.Stop()
	}
}
