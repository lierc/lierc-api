function getTab(tabid) {
  return $('#chats').find('[data-id=' + tabid + ']');
}

function tabId(channel) {
  return window.btoa(channel.toLowerCase()).replace(/=+$/, "");
}

function nextTab() {
  var channels = channelList();
  if (channels.length) {
    return tabId(channels[0]);
  } 
  else {
    return tabList()[0];
  }
}

function focusChannel(channel) {
  if (!channel) channel = channelList()[0];
  if (!channel) return;
  tab = tabId(channel);
  focusTab(tab);
}

function focusTab(tabid) {
  if (!tabid) tabid = nextTab();
  if (!tabid) return;

  var tab = tabs[tabid];
  if (!tab) return;

  $("#chats ol").removeClass("focused");
  $("#chats ol[data-id=" + tabid + "]").addClass("focused");

  scrollToBottom();
  updateCompletions();
  updateStatusLine();
  updateTopic(tab.name, tab.topic);
}

function updateStatusLine() {
  tabid = focusedTab();
  tab = tabs[tabid];
  if (!tab) return;

  var parts = [nick, config.Host + "/" + tab.name];
  var space = Span().append(" ").attr("id", "space");
  var span = Span().append(space);
  span.append(htmlStatusSpan(clock));

  parts.forEach(function(part) {
      span.append(statusSpan(part));
    });

  $("#status").html(span);
  $('#input-prefix').html(Span("[" + tab.name + "]")).append(" ");
}

var clock = Span();
setInterval(updateClockSpan, 1000, clock);
updateClockSpan(clock);

function updateClockSpan(span) {
  var date = new Date();
  span.text(sprintf("%02d:%02d", date.getHours(), date.getMinutes()));
}

function clockSpan() {
  return clock;
}

function statusSpan(text) {
  var span = Span();
  span.append(braceWrapSpan(Span(text), "status"));
  span.append(" ")
  return span;
}

function htmlStatusSpan(html) {
  var span = Span();
  span.append(Span("[", "status-wrap"));
  span.append(html);
  span.append(Span("]", "status-wrap"));
  span.append(" ")
  return span;
}

function focusedTab() {
  return $('#chats ol.focused').attr('data-id');
}

function addChannel(channel) {
  channels[channel] = true;
  addTab(channel, "channel")
}

function tabList() {
  var c = [];
  for (tab in tabs) {
    c.push(tab);
  }
  return c;
}

function channelList() {
  var c = [];
  for (chan in channels) {
    c.push(chan);
  }
  return c;
}

function removeChannel(name) {
  delete(channels[name]);
  removeTab(name);
}

function removeTab(name) {
  var focused = focusedTab();
  tabid = tabId(name)
  $('#chats ol[data-id=' + tabid + ']').remove();
  delete(tabs[tabid]);

  if (focused == tabid)
    focusTab();
}

function addTab(name, type) {
  tabid = tabId(name)
  tabs[tabid] = {type: type, name: name, topic: "No topic set"}

  var el = $('<ol/>', {
      'data-id': tabid
    });
  $('#chats').append(el);

  if (!focusedTab())
    focusTab(tabid);

  fillBacklog(name);
  updateCompletions();
}

function fillBacklog(name, start) {
  if (!start)
    start = 0;

  var url = window.location + "/" + encodeURIComponent(name) + "/" + start + ":";

  $.ajax({
    url: url,
    type: "GET",
    dataType: "json",
    success: function(res) {
      var lines = [];
      res.forEach(function(row) {
        var msg = JSON.parse(row);
        if (msg.Command == "PRIVMSG") {
          var line = buildChatLine(tabid, msg.Prefix[0], msg.Params[1], msg.Time, false);
          lines.unshift(line);
        }
      });

      var tabid = tabId(name);
      prependChatLines(tabid, lines);
    }
  });
}

function Span(text, classname) {
  var span = $('<span/>');
  if (text)
    span.text(text)
  if (classname)
    span.addClass(classname);
  return span;
}

function flexHoriz(l, r) {
  var wrap = $('<div/>', {'class': "flex-wrap"});
  var left = $('<div/>', {'class': "left"});
  var right = $('<div/>', {'class': "right"});
  left.append(l);
  right.append(r);
  wrap.append(left).append(right);
  return wrap;
}

var url_re = /(https?:\/\/[^\s<"]*)/ig;

function linkify(elem) {
  var children = elem.childNodes;
  var length = children.length;

  for (var i=0; i < length; i++) {
    var node = children[i];
    if (node.nodeName == "A") {
      continue;
    }
    else if (node.nodeName != "#text") {
      linkify(node);
    }
    else if (node.nodeValue.match(url_re)) {
      var span = document.createElement("SPAN");
      var escaped = $('<div/>').text(node.nodeValue).html();
      span.innerHTML = escaped.replace(
        url_re, '<a href="$1" target="_blank" rel="noreferrer">$1</a>');
      node.parentNode.replaceChild(span, node);
    }
  }
}

var img_re = /^http[^\s]*\.(?:jpe?g|gif|png|bmp|svg)[^\/]*$/i;
function imagify(elem, tabid) {
  $(elem).find('a').each(function(i, a) {
    a = $(a);
    var href = a.html();
    if (img_re.test(href)) {
      a.on('click', function(e) {
        e.preventDefault();
        var img = $('<img/>');
        img.on('load', function() {
          var scroll = isScrolled(tabid);
          a.html(img);
          var height = img.height();
          var maxheight = Math.min(350, document.documentElement.scrollHeight / 3);
          if (height > maxheight) {
            var width = img.width();
            img.width(Math.floor((maxheight / height) * width));
            img.height(maxheight);
          }
          if (scroll) scrollToBottom();
        });
        img.attr('target', '_blank');
        img.attr('src', href);
      });
    }
  });
}

function prependChatLines(tabid, lines) {
  prependHTMLLines(tabid, lines);
}

function buildChatLine(tabid, nick, text, time, own) {
  var span = Span();
  span.append(timeSpan(time));
  span.append(nickSpan(nick, own));

  var t = Span(text);
  linkify(t.get(0));
  imagify(t, tabid);

  var flex = flexHoriz(span, t);
  return flex;
}

function addChatLine(tabid, nick, text, time, own) {
  var line = buildChatLine(tabid, nick, text, time, own); 
  addHTMLLine(tabid, line);
}

function addActionLine(tabid, nick, text, time, own) {
  var span = Span();
  span.append(timeSpan(time));
  span.append(Span("*" + nick));

  var t = Span(text).css({'text-style':'italic'});;
  linkify(t.get(0));
  imagify(t, tabid);

  var flex = flexHoriz(span, t);
  addHTMLLine(tabid, flex);
}

function addHTMLLine(tabid, html) {
  var scrolled = isScrolled(tabid);

  var tab = getTab(tabid);
  tab.append($('<li/>').html(html));

  if (scrolled)
    scrollToBottom();
}

function prependHTMLLines(tabid, lines) {
  var tab = getTab(tabid);
  var scrolled = isScrolled(tabid);
  var chunk = $('<div/>');
  lines.forEach(function(line) {
    chunk.append($('<li/>').html(line));
  });
  tab.prepend(chunk);
  if (scrolled)
    scrollToBottom();
}

function scrollToBottom() {
  window.scroll(0, document.documentElement.clientHeight);
}

function addHTMLLines(tabid, lines) {
  var scrolled = isScrolled(tabid);

  var tab = getTab(tabid);
  lines.forEach(function(line) {
    tab.append($('<li/>').html(line));
  });

  if (scrolled)
    scrollToBottom();
}

function nickSpan(nick, own) {
  var span = Span()
  span.append(Span("", "nick-wrap").html("&lt;"));
  span.append(" ");
  span.append(Span(nick, own ? "own-nick" : ""));
  span.append(Span("", "nick-wrap").html("&gt;"));
  span.append(" ")
  return span;
}

function timeSpan(time) {
  return Span(timeString(time));
}

function timeString(time) {
  var date = time ? new Date(time * 1000) : new Date();
  return sprintf("%02d:%02d%s", date.getHours(), date.getMinutes(), " ");
}

function isScrolled(tabid) {
  if (focusedTab() != tabid)
    return false;

  if (document.documentElement.scrollHeight <= window.innerHeight)
    return true;

  return window.scrollY == document.documentElement.scrollHeight - window.innerHeight;
}

function addLine(tabid, line, time) {
  var tab = getTab(tabid);
  var scrolled = isScrolled(tabid);

  tab.append($('<li/>').text(timeString(time) + " " + line));

  if (scrolled) {
    $('#chats').scrollTop(tab.height())
  }
}

function infoMark() {
  var span = Span();
  span.append(Span("-", "info-mark"));
  span.append(Span("!"));
  span.append(Span("-", "info-mark"));

}

function addInfoLine(tabid, line, color) {
  var span = Span()

  span.append(timeSpan());
  span.append(infoMark());
  span.append(" ");

  var line = Span(line);
  line.css({color: color});
  span.append(line)
  addHTMLLine(tabid, span);
}

function addNickTable(tabid, nicks) {
  var tab = getTab(tabid);
  var width = tab.width();
  var char = $('#space').width();
  var cols = parseInt(width / char) - 8;
  var space = $('#space').text();

  var max = Math.max.apply(null, nicks.map(
      function(nick) {
        return nick.length }));

  var nick_cols = parseInt(cols / (max + 5));
  var nick_rows = Math.ceil(nicks.length / nick_cols);

  var lines = [];
  for (var i=0; i < nick_rows; i++) {
    var line = Span().append(timeSpan());
    for (var j=0; j < nick_cols; j++) {
      var ind = (i * nick_cols) + j;
      if (ind >= nicks.length)
        break;
      var spaces = space.repeat(max - nicks[ind].length);
      var span = nickListSpan(spaces + nicks[ind]);
      line.append(span).append(" ");
      lines.push(line);
    }
  }

  var title = Span();
  title.append(timeSpan());
  title.append(Span("[Users " + tabs[tabid].name + "]").css({"color":"limegreen"}));
  lines.unshift(title);

  addHTMLLines(tabid, lines);
  var name = tabs[tabid].name;
  addInfoLine(tabid, name + ": " + nicks.length + " nicks"); 
}

function braceWrapSpan(span, classname) {
  var wrap = Span();
  wrap.append(Span("[", classname + "-wrap"));
  wrap.append(span)
  wrap.append(Span("]", classname + "-wrap"));
  return wrap;
}

function nickListSpan(nick) {
  var span = Span();
  span.append(Span("[", "nick-wrap"));
  span.append(" ");
  span.append(Span(nick));
  span.append(" ");
  span.append(Span("]", "nick-wrap"));
  return span;
}

function updateTopic(chan, topic) {
  var tabid = tabId(chan);
  tabs[tabid].topic = topic;
  if (focusedTab() == tabid) {
    $('#topic').text(" " + topic);
  }
}

function addPartLine(tabid, prefix) {
  var tab = tabs[tabid];
  var span = Span();

  span.append(timeSpan())
  span.append(infoMark())
  span.append(" ")

  var nickspan = Span(prefix[0], "part-nick");
  span.append(nickspan);

  if (prefix[1] && prefix[2]) {
    var hostspan = Span(prefix[1] + "@" + prefix[2], "part-host");
    span.append(" ");
    span.append(braceWrapSpan(hostspan, "host"));
  }

  span.append(" has left ");

  addHTMLLine(tabid, span);
}

function addJoinLine(tabid, prefix) {
  var tab = tabs[tabid];
  var span = Span();

  span.append(timeSpan())
  span.append(infoMark())
  span.append(" ")

  var nickspan = Span(prefix[0], "join-nick");
  span.append(nickspan);

  if (prefix[1] && prefix[2]) {
    var hostspan = Span(prefix[1] + "@" + prefix[2], "join-host");
    span.append(" ");
    span.append(braceWrapSpan(hostspan, "host"));
  }

  span.append(" has joined ");

  var chanspan = Span(tab.name, "join-chan");
  span.append(chanspan);

  addHTMLLine(tabid, span);
}

function pruneChatLines() {
  for (tabid in tabs) {
    var tab = getTab(tabid);
    var lines = tab.find("li");
    if (lines.length > 500)
      $(lines.splice(0, lines.length - 500)).remove();

  }
}

setInterval(pruneChatLines, 1000 * 60);
