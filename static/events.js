var events = {
  "375": function(msg) {
    addLine(tabId("status"), msg.Params[1], msg.Time)
  },
  "372": function(msg) {
    addLine(tabId("status"), msg.Params[1], msg.Time)
  },
  "376": function(msg) {
    addLine(tabId("status"), msg.Params[1], msg.Time)
  },
  "422": function(msg) {
    addLine(tabId("status"), msg.Params[1], msg.Time)
  },
  "001": function(msg) {
    addLine(tabId("status"), msg.Params[1], msg.Time)
  },
  "002": function(msg) {
    addLine(tabId("status"), msg.Params[1], msg.Time)
  },
  "003": function(msg) {
    addLine(tabId("status"), msg.Params[1], msg.Time)
  },
  "004": function(msg) {
    addLine(tabId("status"), msg.Params[1], msg.Time)
  },
  "005": function(msg) {
    addLine(tabId("status"), msg.Params[1], msg.Time)
  },
  PRIVMSG: function(msg) {
    var channel = msg.Params[0];
    var from = msg.Prefix[0];
    var text = msg.Params[1];
    if (text.substr(0, 8) == "\x01" + "ACTION ") {
      var action = text.substr(8);
      addActionLine(tabId(channel), from, action, msg.Time, false);
    }
    else {
      addChatLine(tabId(channel), from, text, msg.Time, false);
    }
  },
  JOIN: function(msg) {
    var chan = msg.Params[0];
    if (msg.Prefix[0] == nick) {
      addChannel(chan)
      focusChannel(chan);
    }
    else if (channels[chan]) {
      tabid = tabId(chan);
      if (tabs[tabid] && tabs[tabid].nicks) {
        tabs[tabid].nicks.push(msg.Prefix[0]);
        updateCompletions();
      }
      addJoinLine(tabid, msg.Prefix);
    }
  },
  QUIT: function(msg) {
    var message = msg.Params[0];
    var chan = msg.Params[1];
    if (chan) {
      tabid = tabId(chan)
      if (tabs[tabid] && tabs[tabid].nicks) {
        tabs[tabid].nicks = tabs[tabid].nicks.filter(function(nick) {
            return nick != msg.Prefix[0]});
        updateCompletions();
      }
      addPartLine(tabid, msg.Prefix);
    }
  },
  PART: function(msg) {
    var chan = msg.Params[0];
    if (msg.Prefix[0] == nick) {
      removeChannel(chan);
    }
    else if (channels[chan]) {
      tabid = tabId(chan);
      if (tabs[tabid] && tabs[tabid].nicks) {
        tabs[tabid].nicks = tabs[tabid].nicks.filter(function(nick) {
            return nick != msg.Prefix[0]});
        updateCompletions();
      }
      addPartLine(tabid, msg.Prefix);
    }
  },
  "353": function(msg) {
    var own = msg.Params[0];
    var type = msg.Params[1];
    var chan = msg.Params[2];
    var nicks = msg.Params[3].split(/\s+/);
    var tabid = tabId(chan);
    nickbuffer[tabid] = (nickbuffer[tabid] || []).concat(nicks);
  },
  "366": function(msg) {
    var own = msg.Params[0]
    var chan = msg.Params[1]
    var tab = tabId(chan);

    if (nickbuffer[tab]) {
      tabs[tab]["nicks"] = nickbuffer[tab];
      addNickTable(tab, nickbuffer[tab])
      delete(nickbuffer[tab]);
      if (tab == focusedTab()) {
        updateCompletions();
        updateStatusLine();
      }
    }
  },
  "332": function(msg) {
    var chan = msg.Params[1];
    var topic = msg.Params[2];
    updateTopic(chan, topic);
  },
  "TOPIC": function(msg) {
    var chan = msg.Params[0];
    var topic = msg.Params[1];
    updateTopic(chan, topic);
  },
  NICK: function(msg) {
    var orig = msg.Prefix[0];
    var newnick = msg.Params[0];
    if (orig == nick) {
      nick = newnick;
      updateCompletions();
      updateStatusLine();
    }
  }
};
