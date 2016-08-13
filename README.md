Goals
=====

This is an HTTP API responsible for user authentication, and gating access
to the basic `liercd` functions. It also provides endpoints to access
logs.

Setup
=====

Create database

```
apt-get install postgresql
sudo -u postgres createuser $USER
sudo -u postgres createdb -O $USER lierc
psql lierc < lierc.sql
```

Install carton for perl dependencies

```
apt-get install carton 
carton install
```

Start the server

```
cp config.example.json config.json
carton exec plackup -Ilib app.psgi
```
