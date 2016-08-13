var tabs = {};
var config = init.Config;
var nick = init.Nick;
var channels = {};
var completions = [];
var nickbuffer = {};

$(document).ready(function() {
  connectIRC();

  $('#input input').tabcomplete(completions, {
    minLength: 0,
    hint: false
  });

  addTab("status", "status");
});

function sendRaw(line) {
  $.ajax({
      url: window.location + "/raw",
      type: "POST",
      dataType: "text",
      contentType: "application/irc",
      data: line,
      success: function(res) {
        console.log(res);
      }
    });
}

function updateCompletions() {
  var focused = focusedTab();
  var i = 0;

  channelList().forEach(function(channel) {
      completions[i++] = channel;
    });

  for (command in commands) {
    completions[i++] = "/" + command;
  }

  if (focused && tabs[focused] && tabs[focused].nicks) {
    tabs[focused].nicks.forEach(function(nick) {
        completions[i++] = nick;
      });
  }

  if (completions.length > i)
    completions.splice(i, completions.length);
}
