var remote, retry, timer;


function connectIRC() {
  var nickpart = encodeURIComponent(nick);
  var url = (window.location + "/events/" + nickpart);

  if (remote)
    remote.close();

  remote = new EventSource(url);
  remote.addEventListener('open', function() {
    console.log("connected");
    retry = 0;
  });

  remote.addEventListener('message', function(e) {
    var msg = JSON.parse(e.data);
    if (events[msg.Command]) {
      events[msg.Command](msg);
    }
    else {
      addLine(tabId("status"), msg.Raw, msg.Time);
    }
  });

  if (!timer) {
    function checkConnection() {
      retry++;
      setTimeout(checkConnection, retry * 1000);

      if (!remote || remote.readyState == 2) {
        console.log("reconnecting");
        connectIRC();
      }
      else {
        retry = 0;
      }
    }
    setTimeout(checkConnection, 1000);
  }

  remote.addEventListener('error', function(e) { console.log("error", (new Date())) });
  remote.addEventListener('close', function(e) { console.log("close", (new Date())) });
}
