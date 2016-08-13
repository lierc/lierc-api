var commands = {
  'w': 'window',
  'win': 'window',
  'window': function(args) {
    var tabid = tabId(args);
    focusTab(tabid);
  },
  'clear': function() {
    var tab = getTab(focusedTab());
    tab.html("");
  },
  'part': function(args, text) {
    if (! args != undefined) {
      var tab = tabs[focusedTab()];
      if (tab.type == "channel") {
        sendRaw(sprintf("PART %s", tab.name));
        return; 
      }
    }
    else {
      sendRaw(args, text);
    }
  },
  'join': 'quote',
  'raw': 'quote',
  'quote': function(args, text) {
      sendRaw(text.substr(1, text.length));
  },
  'q': 'quit',
  'quit': function(args) {
    window.location = window.location + "/destroy";
  }
};
