--
-- Production's lierc schema before the migrations baseline (pg_dump
-- --schema-only, 2026-11-16; the \restrict lines removed).
-- TestBaselineMatchesProduction runs baseline.sql on it.
--
-- PostgreSQL database dump
--

-- Dumped from database version 18.6 (Ubuntu 18.6-1.pgdg24.04+2)
-- Dumped by pg_dump version 18.6 (Ubuntu 18.6-1.pgdg24.04+2)

SET statement_timeout = 0;
SET lock_timeout = 0;
SET idle_in_transaction_session_timeout = 0;
SET transaction_timeout = 0;
SET client_encoding = 'UTF8';
SET standard_conforming_strings = on;
SELECT pg_catalog.set_config('search_path', '', false);
SET check_function_bodies = false;
SET xmloption = content;
SET client_min_messages = warning;
SET row_security = off;

--
-- Name: public; Type: SCHEMA; Schema: -; Owner: postgres
--

-- *not* creating schema, since initdb creates it


ALTER SCHEMA public OWNER TO postgres;

--
-- Name: pg_repack; Type: EXTENSION; Schema: -; Owner: -
--

CREATE EXTENSION IF NOT EXISTS pg_repack WITH SCHEMA public;


--
-- Name: EXTENSION pg_repack; Type: COMMENT; Schema: -; Owner:
--

COMMENT ON EXTENSION pg_repack IS 'Reorganize tables in PostgreSQL databases with minimal locks';


--
-- Name: amcheck; Type: EXTENSION; Schema: -; Owner: -
--

CREATE EXTENSION IF NOT EXISTS amcheck WITH SCHEMA public;


--
-- Name: EXTENSION amcheck; Type: COMMENT; Schema: -; Owner:
--

COMMENT ON EXTENSION amcheck IS 'functions for verifying relation integrity';


--
-- Name: pg_trgm; Type: EXTENSION; Schema: -; Owner: -
--

CREATE EXTENSION IF NOT EXISTS pg_trgm WITH SCHEMA public;


--
-- Name: EXTENSION pg_trgm; Type: COMMENT; Schema: -; Owner:
--

COMMENT ON EXTENSION pg_trgm IS 'text similarity measurement and index searching based on trigrams';


SET default_tablespace = '';

SET default_table_access_method = heap;

--
-- Name: connection; Type: TABLE; Schema: public; Owner: lierc
--

CREATE TABLE public.connection (
    "user" character varying(24) NOT NULL,
    id character varying(24) NOT NULL,
    config jsonb NOT NULL,
    enabled boolean DEFAULT true NOT NULL,
    liercd text DEFAULT 'next'::text,
    created timestamp without time zone,
    updated timestamp without time zone
);


ALTER TABLE public.connection OWNER TO lierc;

--
-- Name: log; Type: TABLE; Schema: public; Owner: lierc
--

CREATE TABLE public.log (
    id integer NOT NULL,
    connection character varying(24) NOT NULL,
    channel character varying(32) NOT NULL,
    "time" timestamp without time zone NOT NULL,
    message jsonb NOT NULL,
    self boolean DEFAULT false NOT NULL,
    highlight boolean DEFAULT false,
    command character varying(16),
    "user" character varying(24) NOT NULL
);


ALTER TABLE public.log OWNER TO lierc;

--
-- Name: log_archive; Type: TABLE; Schema: public; Owner: lierc
--

CREATE TABLE public.log_archive (
    id integer NOT NULL,
    connection character varying(24) NOT NULL,
    "user" character varying(24) NOT NULL,
    channel character varying(32) NOT NULL,
    first_id integer NOT NULL,
    last_id integer NOT NULL,
    min_time timestamp without time zone NOT NULL,
    max_time timestamp without time zone NOT NULL,
    count integer NOT NULL,
    messages json NOT NULL
);
ALTER TABLE ONLY public.log_archive ALTER COLUMN messages SET COMPRESSION lz4;


ALTER TABLE public.log_archive OWNER TO lierc;

--
-- Name: log_archive_id_seq; Type: SEQUENCE; Schema: public; Owner: lierc
--

CREATE SEQUENCE public.log_archive_id_seq
    AS integer
    START WITH 1
    INCREMENT BY 1
    NO MINVALUE
    NO MAXVALUE
    CACHE 1;


ALTER SEQUENCE public.log_archive_id_seq OWNER TO lierc;

--
-- Name: log_archive_id_seq; Type: SEQUENCE OWNED BY; Schema: public; Owner: lierc
--

ALTER SEQUENCE public.log_archive_id_seq OWNED BY public.log_archive.id;


--
-- Name: log_id_seq; Type: SEQUENCE; Schema: public; Owner: lierc
--

CREATE SEQUENCE public.log_id_seq
    START WITH 1
    INCREMENT BY 1
    NO MINVALUE
    NO MAXVALUE
    CACHE 1;


ALTER SEQUENCE public.log_id_seq OWNER TO lierc;

--
-- Name: log_id_seq; Type: SEQUENCE OWNED BY; Schema: public; Owner: lierc
--

ALTER SEQUENCE public.log_id_seq OWNED BY public.log.id;


--
-- Name: login; Type: TABLE; Schema: public; Owner: lierc
--

CREATE TABLE public.login (
    id bigint NOT NULL,
    token_hash bytea NOT NULL,
    "user" character varying(24) NOT NULL,
    created timestamp with time zone DEFAULT now() NOT NULL,
    last_used timestamp with time zone DEFAULT now() NOT NULL,
    user_agent text DEFAULT ''::text NOT NULL,
    ip inet
);


ALTER TABLE public.login OWNER TO lierc;

--
-- Name: login_id_seq; Type: SEQUENCE; Schema: public; Owner: lierc
--

ALTER TABLE public.login ALTER COLUMN id ADD GENERATED ALWAYS AS IDENTITY (
    SEQUENCE NAME public.login_id_seq
    START WITH 1
    INCREMENT BY 1
    NO MINVALUE
    NO MAXVALUE
    CACHE 1
);


--
-- Name: pref; Type: TABLE; Schema: public; Owner: lierc
--

CREATE TABLE public.pref (
    "user" character varying(24) NOT NULL,
    name character varying(128) NOT NULL,
    value character varying(512)
);


ALTER TABLE public.pref OWNER TO lierc;

--
-- Name: private; Type: TABLE; Schema: public; Owner: lierc
--

CREATE TABLE public.private (
    connection character varying(24) NOT NULL,
    nick character varying(32) NOT NULL,
    "time" timestamp without time zone
);


ALTER TABLE public.private OWNER TO lierc;

--
-- Name: seen; Type: TABLE; Schema: public; Owner: lierc
--

CREATE TABLE public.seen (
    "user" character varying(24) NOT NULL,
    connection character varying(24) NOT NULL,
    channel character varying(32) NOT NULL,
    message_id integer NOT NULL,
    updated timestamp without time zone DEFAULT now() NOT NULL
);


ALTER TABLE public.seen OWNER TO lierc;

--
-- Name: user; Type: TABLE; Schema: public; Owner: lierc
--

CREATE TABLE public."user" (
    id character varying(24) NOT NULL,
    email character varying(256) NOT NULL,
    last_id integer DEFAULT 0 NOT NULL,
    last_login timestamp without time zone,
    username character varying(255) NOT NULL,
    verified boolean DEFAULT false,
    password_hash character varying(60),
    verify_sent timestamp without time zone,
    registered timestamp without time zone,
    CONSTRAINT user_username_valid CHECK (((username)::text ~ '^[a-zA-Z0-9_.]{1,32}$'::text))
);


ALTER TABLE public."user" OWNER TO lierc;

--
-- Name: web_push; Type: TABLE; Schema: public; Owner: lierc
--

CREATE TABLE public.web_push (
    endpoint character varying(255) NOT NULL,
    key character varying(255) NOT NULL,
    auth character varying(255) NOT NULL,
    "user" character varying(24) NOT NULL
);


ALTER TABLE public.web_push OWNER TO lierc;

--
-- Name: log id; Type: DEFAULT; Schema: public; Owner: lierc
--

ALTER TABLE ONLY public.log ALTER COLUMN id SET DEFAULT nextval('public.log_id_seq'::regclass);


--
-- Name: log_archive id; Type: DEFAULT; Schema: public; Owner: lierc
--

ALTER TABLE ONLY public.log_archive ALTER COLUMN id SET DEFAULT nextval('public.log_archive_id_seq'::regclass);


--
-- Name: connection connection_pkey; Type: CONSTRAINT; Schema: public; Owner: lierc
--

ALTER TABLE ONLY public.connection
    ADD CONSTRAINT connection_pkey PRIMARY KEY (id);


--
-- Name: log_archive log_archive_pkey; Type: CONSTRAINT; Schema: public; Owner: lierc
--

ALTER TABLE ONLY public.log_archive
    ADD CONSTRAINT log_archive_pkey PRIMARY KEY (id);


--
-- Name: log log_pkey; Type: CONSTRAINT; Schema: public; Owner: lierc
--

ALTER TABLE ONLY public.log
    ADD CONSTRAINT log_pkey PRIMARY KEY (id);


--
-- Name: login login_pkey; Type: CONSTRAINT; Schema: public; Owner: lierc
--

ALTER TABLE ONLY public.login
    ADD CONSTRAINT login_pkey PRIMARY KEY (id);


--
-- Name: login login_token_hash_key; Type: CONSTRAINT; Schema: public; Owner: lierc
--

ALTER TABLE ONLY public.login
    ADD CONSTRAINT login_token_hash_key UNIQUE (token_hash);


--
-- Name: pref pref_pkey; Type: CONSTRAINT; Schema: public; Owner: lierc
--

ALTER TABLE ONLY public.pref
    ADD CONSTRAINT pref_pkey PRIMARY KEY ("user", name);


--
-- Name: private private_pkey; Type: CONSTRAINT; Schema: public; Owner: lierc
--

ALTER TABLE ONLY public.private
    ADD CONSTRAINT private_pkey PRIMARY KEY (connection, nick);


--
-- Name: seen seen_pkey; Type: CONSTRAINT; Schema: public; Owner: lierc
--

ALTER TABLE ONLY public.seen
    ADD CONSTRAINT seen_pkey PRIMARY KEY ("user", connection, channel);


--
-- Name: user user_pkey; Type: CONSTRAINT; Schema: public; Owner: lierc
--

ALTER TABLE ONLY public."user"
    ADD CONSTRAINT user_pkey PRIMARY KEY (id);


--
-- Name: web_push web_push_pkey; Type: CONSTRAINT; Schema: public; Owner: lierc
--

ALTER TABLE ONLY public.web_push
    ADD CONSTRAINT web_push_pkey PRIMARY KEY ("user", endpoint);


--
-- Name: connection_user_idx; Type: INDEX; Schema: public; Owner: lierc
--

CREATE INDEX connection_user_idx ON public.connection USING btree ("user");


--
-- Name: log_archive_connection_channel_last_id_idx; Type: INDEX; Schema: public; Owner: lierc
--

CREATE INDEX log_archive_connection_channel_last_id_idx ON public.log_archive USING btree (connection, channel, last_id DESC);


--
-- Name: log_archive_connection_channel_min_time_idx; Type: INDEX; Schema: public; Owner: lierc
--

CREATE INDEX log_archive_connection_channel_min_time_idx ON public.log_archive USING btree (connection, channel, min_time);


--
-- Name: log_connection_channel_id_idx; Type: INDEX; Schema: public; Owner: lierc
--

CREATE INDEX log_connection_channel_id_idx ON public.log USING btree (connection, channel, id DESC);


--
-- Name: log_message_text; Type: INDEX; Schema: public; Owner: lierc
--

CREATE INDEX log_message_text ON public.log USING gin (to_tsvector('english'::regconfig, ((message -> 'Params'::text) -> 1)));


--
-- Name: log_sender; Type: INDEX; Schema: public; Owner: lierc
--

CREATE INDEX log_sender ON public.log USING gin ((((message -> 'Prefix'::text) -> 'Name'::text)));


--
-- Name: log_time_idx; Type: INDEX; Schema: public; Owner: lierc
--

CREATE INDEX log_time_idx ON public.log USING btree ("time");


--
-- Name: log_user_highlight_idx; Type: INDEX; Schema: public; Owner: lierc
--

CREATE INDEX log_user_highlight_idx ON public.log USING btree ("user", highlight, id DESC);


--
-- Name: login_user; Type: INDEX; Schema: public; Owner: lierc
--

CREATE INDEX login_user ON public.login USING btree ("user");


--
-- Name: user_email_lower_key; Type: INDEX; Schema: public; Owner: lierc
--

CREATE UNIQUE INDEX user_email_lower_key ON public."user" USING btree (lower((email)::text));


--
-- Name: user_username_lower_key; Type: INDEX; Schema: public; Owner: lierc
--

CREATE UNIQUE INDEX user_username_lower_key ON public."user" USING btree (lower((username)::text));


--
-- Name: login login_user_fkey; Type: FK CONSTRAINT; Schema: public; Owner: lierc
--

ALTER TABLE ONLY public.login
    ADD CONSTRAINT login_user_fkey FOREIGN KEY ("user") REFERENCES public."user"(id) ON DELETE CASCADE;


--
-- PostgreSQL database dump complete
--
