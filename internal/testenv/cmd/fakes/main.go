// Command fakes runs the testenv stand-ins for liercd, SMTP and Imgur as one
// standalone process, for the Perl/Go parity suite (parity/compose.yml).
//
//	fakes                   serve every fake (flags below)
//	fakes schema lierc.sql  print lierc.sql with testenv's syntax patches
//
// A control API lets the suite script and inspect the fakes:
//
//	GET  /health          200 "ok"
//	POST /reset           forget all liercd, SMTP and Imgur state
//	POST /liercd/respond  {"method","path","code","body"}: force a response
//	POST /liercd/status   {"id","status"}: status JSON returned for id
//	GET  /liercd/calls    calls received, oldest first
//	GET  /smtp/messages   messages accepted, oldest first
//	GET  /imgur/uploads   uploads received, oldest first
package main

import (
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"log"
	"net"
	"net/http"
	"os"

	"github.com/lierc/lierc-api/internal/testenv"
)

func main() {
	if len(os.Args) > 1 && os.Args[1] == "schema" {
		if len(os.Args) != 3 {
			fmt.Fprintln(os.Stderr, "usage: fakes schema <lierc.sql>")
			os.Exit(2)
		}
		if err := printSchema(os.Args[2], os.Stdout); err != nil {
			fmt.Fprintln(os.Stderr, err)
			os.Exit(1)
		}
		return
	}

	liercdAddr := flag.String("liercd", ":5005", "fake liercd listen address")
	smtpAddr := flag.String("smtp", ":2525", "fake SMTP listen address")
	imgurAddr := flag.String("imgur", ":8081", "fake Imgur listen address")
	controlAddr := flag.String("control", ":8090", "control API listen address")
	flag.Parse()

	lc := testenv.NewLiercd()
	smtp := testenv.NewSMTPServer("", "")
	imgur := testenv.NewImgur()

	ln, err := net.Listen("tcp", *smtpAddr)
	if err != nil {
		log.Fatalf("smtp: %v", err)
	}
	go smtp.Serve(ln)

	errs := make(chan error, 3)
	serve := func(name, addr string, h http.Handler) {
		log.Printf("fake %s listening on %s", name, addr)
		errs <- fmt.Errorf("%s: %w", name, http.ListenAndServe(addr, h))
	}
	go serve("liercd", *liercdAddr, lc)
	go serve("imgur", *imgurAddr, imgur)
	go serve("control", *controlAddr, control(lc, smtp, imgur))
	log.Printf("fake smtp listening on %s", *smtpAddr)
	log.Fatal(<-errs)
}

func printSchema(path string, w io.Writer) error {
	b, err := os.ReadFile(path)
	if err != nil {
		return err
	}
	s, err := testenv.PatchSchema(string(b))
	if err != nil {
		return err
	}
	_, err = io.WriteString(w, s)
	return err
}

func control(lc *testenv.LiercdServer, smtp *testenv.SMTPServer, imgur *testenv.Imgur) http.Handler {
	mux := http.NewServeMux()
	writeJSON := func(w http.ResponseWriter, v any) {
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(v)
	}
	mux.HandleFunc("GET /health", func(w http.ResponseWriter, r *http.Request) {
		io.WriteString(w, "ok")
	})
	mux.HandleFunc("POST /reset", func(w http.ResponseWriter, r *http.Request) {
		lc.Reset()
		smtp.Reset()
		imgur.Reset()
		w.WriteHeader(http.StatusNoContent)
	})
	mux.HandleFunc("POST /liercd/respond", func(w http.ResponseWriter, r *http.Request) {
		var req struct {
			Method, Path, Body string
			Code               int
		}
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		lc.Respond(req.Method, req.Path, req.Code, req.Body)
		w.WriteHeader(http.StatusNoContent)
	})
	mux.HandleFunc("POST /liercd/status", func(w http.ResponseWriter, r *http.Request) {
		var req struct{ ID, Status string }
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		lc.SetStatus(req.ID, req.Status)
		w.WriteHeader(http.StatusNoContent)
	})
	mux.HandleFunc("GET /liercd/calls", func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, lc.Calls())
	})
	mux.HandleFunc("GET /smtp/messages", func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, smtp.Received())
	})
	mux.HandleFunc("GET /imgur/uploads", func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, imgur.Uploads())
	})
	return mux
}
