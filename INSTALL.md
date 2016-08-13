set database

```
apt-get install postgresql
sudo -u postgres createuser $USER
sudo -u postgres createdb -O $USER lierc
psql lierc < lierc.sql
```

install carton for perl dependencies

```
apt-get install carton 
carton install
```

start the server

```
cp config.example.json config.json
carton exec plackup -Ilib app.psgi
```
