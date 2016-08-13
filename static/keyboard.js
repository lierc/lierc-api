$(document).ready(function() {
  var input = $('#input input');

  $(document).on('keydown', function(e) {
    if (e.which == 16 || e.which == 17 || e.which == 18 || e.which == 91)
      return;

    if (!$('input:focus').length)
      input.focus();
  });

  input.on('keypress', function(e) {
    if (e.which != 13) return;
    e.preventDefault();

    var tabid = focusedTab()
    var text = input.val();
    input.val("");

    if (text.substr(0,1) == "/") {
      var match = text.match(/^\/([^ ]+)(?:\s(.+))?$/)
      var command = match[1].toLowerCase();

      if (commands[command]) {
        var args = match[2];
        if (typeof(commands[command]) == "string") {
          commands[commands[command]](args, text);
        }
        else {
          commands[command](args, text);
        }
      }
      else if (command) {
        sendRaw(text.substr(1, text.length));
      }
    }
    else if (tabid) {
      var chan = tabs[tabid].name;
      sendRaw("PRIVMSG " + chan + " :" + text);
      addChatLine(tabid, nick, text, true);
    }
    else {
      alert("can't chat here");
    }
  });

  input.focus();
});
