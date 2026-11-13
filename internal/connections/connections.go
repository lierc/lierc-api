// Package connections creates, edits, saves and deletes a user's IRC
// connections: the row, liercd, and telling the user's open tabs. It is
// ported from lib/API/Controller/Connection.pm and keeps its order of
// steps, which is where the care is.
package connections

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"time"

	"github.com/lierc/lierc-api/internal/connconfig"
	"github.com/lierc/lierc-api/internal/db"
	"github.com/lierc/lierc-api/internal/ids"
	"github.com/lierc/lierc-api/internal/liercd"
	"github.com/lierc/lierc-api/internal/respond"
)

// UserError is a problem to show the user, with Perl's message.
type UserError struct{ Msg string }

func (e *UserError) Error() string { return e.Msg }

// IsUserError is err's message for the user, if it has one.
func IsUserError(err error) (string, bool) {
	var ue *UserError
	if errors.As(err, &ue) {
		return ue.Msg, true
	}
	return "", false
}

func userError(msg string) error { return &UserError{strings.TrimRight(msg, "\r\n")} }

var errInvalid = &UserError{"Invalid connection settings"}

// Service does the work, through the database, liercd, and Announce,
// which tells the owner's open streams (the stream server, in memory).
type Service struct {
	DB               db.Store
	Liercd           *liercd.Client
	Announce         func(ctx context.Context, c db.ConnectionChange)
	NotifyHighlights func(ctx context.Context) error
	CheckHost        func(ctx context.Context, host string, port int) error
	// Limit is how many connections a user can have (API::Limits).
	Limit int
}

// NotifyHighlights tells the logger's notifier, a separate process, to
// reload highlight words.
func NotifyHighlights(d db.Store) func(ctx context.Context) error {
	return d.NotifyHighlights
}

// stepTimeout bounds a create, edit, save or delete.
const stepTimeout = time.Minute

// detached is ctx without its cancellation, bounded by stepTimeout: once
// started, the steps finish even if the browser goes away, as Perl's did.
// A cancelled request mustn't stop an undo, or leave a destroyed
// connection un-recreated.
func detached(ctx context.Context) (context.Context, context.CancelFunc) {
	return context.WithTimeout(context.WithoutCancel(ctx), stepTimeout)
}

// newID makes connection ids (Util->uuid); tests fix it.
var newID = ids.New

// object decodes raw as a JSON object, or fails with Perl's message.
func object(raw []byte) (map[string]any, error) {
	v, err := connconfig.Decode(raw)
	if m, ok := v.(map[string]any); err == nil && ok {
		return m, nil
	}
	return nil, errInvalid
}

// clean is Clean plus the host check, with problems for the user.
func (s *Service) clean(ctx context.Context, raw []byte) (map[string]any, error) {
	config, err := connconfig.Clean(raw)
	if err != nil {
		return nil, userError(err.Error())
	}
	if err := s.CheckHost(ctx, config["Host"].(string), config["Port"].(int)); err != nil {
		return nil, userError(err.Error())
	}
	return config, nil
}

// liercd posts body to liercd's path and returns the status, or a user
// error when liercd can't be reached (Perl showed LWP's text).
func (s *Service) liercd(ctx context.Context, path string, body []byte) (*liercd.Response, error) {
	res, err := s.Liercd.Do(ctx, http.MethodPost, path, body)
	if err != nil {
		return nil, userError(err.Error())
	}
	return res, nil
}

func (s *Service) announce(ctx context.Context, user, id, action string) {
	s.Announce(ctx, db.ConnectionChange{User: user, Connection: id, Action: action})
}

// Create saves a new connection and starts it in liercd (Connection.pm
// create and start). The row is saved first: which liercd it goes to is
// looked up from the row, and the stream sends its first events to whoever
// the row says owns it. The create is announced before liercd connects, so
// the owner's tabs have it before its first status can arrive; a create
// liercd refuses is deleted and announced as deleted again.
func (s *Service) Create(ctx context.Context, user string, raw []byte) (string, error) {
	ctx, cancel := detached(ctx)
	defer cancel()
	if _, err := object(raw); err != nil {
		return "", err
	}
	config, err := s.clean(ctx, raw)
	if err != nil {
		return "", err
	}
	body, err := respond.Marshal(config)
	if err != nil {
		return "", err
	}
	id := newID()
	saved, err := s.DB.SaveConnectionLimited(ctx, id, user, body, s.Limit)
	if err != nil {
		return "", err
	}
	if !saved {
		return "", userError(fmt.Sprintf("You can have at most %d connections", s.Limit))
	}
	s.announce(ctx, user, id, "create")

	res, err := s.liercd(ctx, id+"/create", body)
	if err == nil && res.Status != http.StatusOK {
		err = userError(string(res.Body))
	}
	if err != nil {
		if derr := s.DB.DeleteConnection(ctx, id); derr != nil {
			return "", derr
		}
		s.announce(ctx, user, id, "delete")
		return "", err
	}
	return id, s.NotifyHighlights(ctx)
}

// CreateDefault adds the signup network (Auth.pm add_default_network). It
// differs from Create in exactly these ways: the config is saved as given
// (no Clean, no limit, no host check), nothing is announced, and the row
// stays when liercd fails, as in Perl; the stream re-creates it when the
// user next connects. On a liercd failure the id comes back with the error.
func (s *Service) CreateDefault(ctx context.Context, user string, config map[string]any) (string, error) {
	ctx, cancel := detached(ctx)
	defer cancel()
	body, err := respond.Marshal(config)
	if err != nil {
		return "", err
	}
	id := newID()
	if err := s.DB.SaveConnection(ctx, id, user, body); err != nil {
		return "", err
	}
	if err := s.NotifyHighlights(ctx); err != nil {
		return id, err
	}
	res, err := s.liercd(ctx, id+"/create", body)
	if err == nil && res.Status != http.StatusOK {
		err = userError(string(res.Body))
	}
	return id, err
}

// saved is connection id's stored config as an object, or empty.
func (s *Service) saved(ctx context.Context, id string) (map[string]any, error) {
	raw, ok, err := s.DB.LookupConfig(ctx, id)
	if err != nil || !ok {
		return map[string]any{}, err
	}
	v, _ := connconfig.Decode(raw)
	m, ok := v.(map[string]any)
	if !ok {
		return map[string]any{}, nil
	}
	return m, nil
}

// edited is the request's settings for an existing connection, with the
// saved channel list (Connection.pm edited_config). Channels are only set
// when a connection is created; after that, joining and leaving change
// them. A request without a nick keeps the saved one, which isn't
// re-checked, so an old nick today's rules refuse can't block an edit.
func (s *Service) edited(ctx context.Context, id string, raw []byte, saved map[string]any) (map[string]any, error) {
	params, err := object(raw)
	if err != nil {
		return nil, err
	}
	nick, _ := saved["Nick"].(string)
	delete(params, "Channels")
	given, _ := params["Nick"].(string)
	keep := nick != "" && (params["Nick"] == nil || given == "")
	if keep {
		params["Nick"] = "placeholder"
	}
	rebuilt, err := json.Marshal(params)
	if err != nil {
		return nil, errInvalid
	}
	config, err := s.clean(ctx, rebuilt)
	if err != nil {
		return nil, err
	}
	if keep {
		config["Nick"] = nick
	}
	if channels, ok := saved["Channels"].([]any); ok {
		config["Channels"] = channels
	} else {
		config["Channels"] = []any{}
	}
	return config, nil
}

// Edit changes a connection's settings (Connection.pm edit). Everything is
// checked before the running connection is touched. If nothing liercd
// connects with changed, the settings are just saved; otherwise liercd
// destroys and recreates it, and the row stays put meanwhile, so the
// stream always finds its owner.
func (s *Service) Edit(ctx context.Context, user, id string, raw []byte) error {
	ctx, cancel := detached(ctx)
	defer cancel()
	saved, err := s.saved(ctx, id)
	if err != nil {
		return err
	}
	config, err := s.edited(ctx, id, raw, saved)
	if err != nil {
		return err
	}
	body, err := respond.Marshal(config)
	if err != nil {
		return err
	}
	if len(saved) > 0 && serverKey(saved) == serverKey(config) {
		return s.updated(ctx, user, id, body)
	}

	res, err := s.liercd(ctx, id+"/destroy", nil)
	if err != nil {
		return err
	}
	if res.Status != http.StatusOK && res.Status != http.StatusNotFound {
		return userError(string(res.Body))
	}
	if err := s.DB.UpdateConfig(ctx, id, body); err != nil {
		return err
	}
	res, err = s.liercd(ctx, id+"/create", body)
	if err != nil {
		return err
	}
	if res.Status != http.StatusOK {
		return userError(string(res.Body))
	}
	if err := s.NotifyHighlights(ctx); err != nil {
		return err
	}
	s.announce(ctx, user, id, "update")
	return nil
}

// updated saves body as id's config, reloads highlights and announces it.
func (s *Service) updated(ctx context.Context, user, id string, body []byte) error {
	if err := s.DB.UpdateConfig(ctx, id, body); err != nil {
		return err
	}
	if err := s.NotifyHighlights(ctx); err != nil {
		return err
	}
	s.announce(ctx, user, id, "update")
	return nil
}

// Save changes a connection's highlight words and alias, which don't need
// a reconnect (Connection.pm save). The rest of the saved config is kept as
// it is: it's what liercd is running, and an old value or a DNS failure
// can't block this.
func (s *Service) Save(ctx context.Context, user, id string, raw []byte) error {
	ctx, cancel := detached(ctx)
	defer cancel()
	params, err := object(raw)
	if err != nil {
		return err
	}
	names, err := connconfig.CleanNames(params)
	if err != nil {
		return userError(err.Error())
	}
	stored, ok, err := s.DB.LookupConfig(ctx, id)
	if err != nil {
		return err
	}
	if !ok {
		return errInvalid
	}
	config, err := object(stored)
	if err != nil {
		return err
	}
	delete(config, "Alias")
	for k, v := range names {
		config[k] = v
	}
	body, err := respond.Marshal(config)
	if err != nil {
		return err
	}
	return s.updated(ctx, user, id, body)
}

// Delete destroys a connection in liercd and deletes its row (Connection.pm
// delete). A connection liercd no longer has (404) is deleted too: Perl
// refused, so a network liercd had lost could never be removed.
func (s *Service) Delete(ctx context.Context, user, id string) error {
	ctx, cancel := detached(ctx)
	defer cancel()
	res, err := s.liercd(ctx, id+"/destroy", nil)
	if err != nil {
		return err
	}
	if res.Status != http.StatusOK && res.Status != http.StatusNotFound {
		return userError(string(res.Body))
	}
	if err := s.DB.DeleteConnection(ctx, id); err != nil {
		return err
	}
	s.announce(ctx, user, id, "delete")
	return nil
}
