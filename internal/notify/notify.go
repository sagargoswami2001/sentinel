// Package notify sends up/down alerts to messaging platforms.
//
// Everything is built around the Notifier interface: one method,
// one message. Adding a new platform means one new type with a Send
// method plus one line in New — nothing else changes.
package notify

import (
	"bytes"
	"encoding/json"
	"fmt"
	"log"
	"net/http"
	"strings"
	"time"
)

// Event is one up/down transition worth telling someone about.
type Event struct {
	Name   string `json:"name"` // target name
	URL    string `json:"url"`
	Down   bool   `json:"down"` // true = went down, false = recovered
	Detail string `json:"detail"`
	At     time.Time `json:"at"`
}

func (e Event) text() string {
	icon, verb := "🔴", "is DOWN"
	if !e.Down {
		icon, verb = "🟢", "is back UP"
	}
	s := fmt.Sprintf("%s %s %s\n%s", icon, e.Name, verb, e.URL)
	if e.Detail != "" {
		s += "\n" + e.Detail
	}
	return s
}

// Notifier delivers one event to one destination.
type Notifier interface {
	Send(e Event) error
}

// New builds a notifier for a platform key. Keys match Platforms.
func New(platform, webhookURL string) (Notifier, error) {
	if !strings.HasPrefix(webhookURL, "http://") && !strings.HasPrefix(webhookURL, "https://") {
		return nil, fmt.Errorf("webhook URL must start with http:// or https://")
	}
	switch platform {
	case "slack":
		return Slack{webhookURL}, nil
	case "teams":
		return Teams{webhookURL}, nil
	case "googlechat":
		return GoogleChat{webhookURL}, nil
	case "discord":
		return Discord{webhookURL}, nil
	case "webhook":
		return Webhook{webhookURL}, nil
	default:
		return nil, fmt.Errorf("unknown platform %q", platform)
	}
}

// client is shared by all notifiers; webhooks should answer fast.
var client = &http.Client{Timeout: 10 * time.Second}

func post(url string, payload any) error {
	body, err := json.Marshal(payload)
	if err != nil {
		return err
	}
	resp, err := client.Post(url, "application/json", bytes.NewReader(body))
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode >= 300 {
		return fmt.Errorf("webhook returned %s", resp.Status)
	}
	return nil
}

// Slack — incoming webhook (https://api.slack.com/messaging/webhooks).
type Slack struct{ WebhookURL string }

func (n Slack) Send(e Event) error {
	// Slack renders *bold* and `code` markdown.
	icon, verb := "🔴", "is DOWN"
	if !e.Down {
		icon, verb = "🟢", "is back UP"
	}
	text := fmt.Sprintf("%s *%s* %s\n%s", icon, e.Name, verb, e.URL)
	if e.Detail != "" {
		text += "\n`" + e.Detail + "`"
	}
	return post(n.WebhookURL, map[string]string{"text": text})
}

// Teams — incoming webhook / Workflows URL, Adaptive Card payload.
type Teams struct{ WebhookURL string }

func (n Teams) Send(e Event) error {
	title, color := "🔴 "+e.Name+" is DOWN", "attention"
	if !e.Down {
		title, color = "🟢 "+e.Name+" is back UP", "good"
	}
	card := map[string]any{
		"type": "message",
		"attachments": []any{map[string]any{
			"contentType": "application/vnd.microsoft.card.adaptive",
			"content": map[string]any{
				"$schema": "http://adaptivecards.io/schemas/adaptive-card.json",
				"type":    "AdaptiveCard",
				"version": "1.4",
				"body": []any{
					map[string]any{
						"type": "TextBlock", "text": title,
						"weight": "bolder", "size": "medium", "color": color,
					},
					map[string]any{"type": "TextBlock", "text": e.URL, "spacing": "none"},
					map[string]any{"type": "TextBlock", "text": e.Detail, "wrap": true, "isSubtle": true},
				},
			},
		}},
	}
	return post(n.WebhookURL, card)
}

// GoogleChat — space webhook
// (https://developers.google.com/chat/how-tos/webhooks).
type GoogleChat struct{ WebhookURL string }

func (n GoogleChat) Send(e Event) error {
	return post(n.WebhookURL, map[string]string{"text": e.text()})
}

// Discord — channel webhook (Server Settings -> Integrations -> Webhooks).
type Discord struct{ WebhookURL string }

func (n Discord) Send(e Event) error {
	return post(n.WebhookURL, map[string]string{"content": e.text()})
}

// Webhook — plain JSON POST of the raw event, for anything custom:
// PagerDuty bridges, n8n/Zapier flows, your own service.
type Webhook struct{ URL string }

func (n Webhook) Send(e Event) error {
	return post(n.URL, e)
}

// All sends an event to every notifier concurrently. Failures are
// logged, never fatal — a broken webhook must not stop monitoring.
func All(notifiers []Notifier, e Event) {
	for _, n := range notifiers {
		go func(n Notifier) {
			if err := n.Send(e); err != nil {
				log.Printf("notify: %T failed: %v", n, err)
			}
		}(n)
	}
}
