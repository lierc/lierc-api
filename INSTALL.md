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

install bower to install JS dependencies

```
apt-get install npm 
npm install bower
nodejs node_modules/.bin/bower install
```

start the server

```
cp config.example.json config.json
./start.sh
```
