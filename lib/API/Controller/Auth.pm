package API::Controller::Auth;

use parent 'API::Controller';

use Util;
use API::SignedToken;
use JSON::XS;
use Data::Validate::Email;
use MIME::Lite;
use Net::SMTP;
use Text::Xslate;

API->register("auth.show",     __PACKAGE__);
API->register("auth.login",    __PACKAGE__);
API->register("auth.register", __PACKAGE__);
API->register("auth.logout",   __PACKAGE__);
API->register("auth.verify",   __PACKAGE__);
API->register("auth.reset",    __PACKAGE__);
API->register("auth.reset_confirm", __PACKAGE__);

my $tx = Text::Xslate->new;
my $verify_template = q{Hello <: $name :>,

Thanks for signing up with relaychat.party! Please confirm your email
address by opening this link:
<: $link :>

Happy chatting!};

my $verify_invalid = "This confirmation link has expired or isn't valid.";

my $reset_template = q{Hello <: $name :>,

Someone asked to reset the password for your relaychat.party account.
To choose a new password, open this link within the next hour:
<: $link :>

If you didn't ask for this, you can ignore this email; your password
hasn't changed.};

my $reset_expired = "This reset link has expired or was already used. Request a new one.";

sub show {
  my ($app, $req) = @_;
  my $user = $app->lookup_user($req->session->{user});
  return $app->json({
    email => $user->{email},
    user => $user->{username},
    id => $user->{id},
  });
}

# Checked when no user matches, so the response takes as long as a real check.
my $DUMMY_HASH = '$2b$12$iTKU3aaMiT3Vi52s2w3Xn.2gy1gF6/6pKUgHg/.dMCAsrp43MwrkG';

sub login {
  my ($app, $req) = @_;

  my $pass  = $req->parameters->{pass};
  my $email = $req->parameters->{email};

  # usually one row; more if one user's email is another's username
  my $rows = $app->dbh->selectall_arrayref(
    q{SELECT id, password, password_hash FROM "user" WHERE lower(email) = lower(?) OR lower(username) = lower(?)},
    { Slice => {} }, $email, $email
  );

  Util->check_password_hash($pass, $DUMMY_HASH) unless @$rows;

  for my $row (@$rows) {
    if (defined $row->{password_hash}) {
      next unless Util->check_password_hash($pass, $row->{password_hash});
    }
    else {
      my $legacy = Util->hash_password($pass, $app->secret);
      next unless Util->secure_eq($legacy, $row->{password});
      $app->set_password_hash($row->{id}, Util->new_password_hash($pass));
    }

    $req->session->{user} = $row->{id};
    return $app->handle("auth.show", $req);
  }

  return $app->unauthorized("Invalid email or password");
}

sub logout {
  my ($app, $req) = @_;
  delete $req->env->{'psgix.session'}->{user};
  return $app->ok;
}

sub register {
  my ($app, $req) = @_;

  for (qw(username email pass)) {
    die "$_ is required"
      unless defined $req->parameters->{$_}
        && $req->parameters->{$_} =~ /\S/;
  }

  # stored as typed; unique and looked up regardless of case
  die "Usernames can only use letters, digits, _ and ., up to 32 characters\n"
    unless $req->parameters->{username} =~ /\A[a-zA-Z0-9_.]{1,32}\z/;

  # stored lowercase; unique and looked up regardless of case
  my $email = lc $req->parameters->{email};
  my $pass = $req->parameters->{pass};
  my $user = $req->parameters->{username};

  die "Invalid email address"
    unless Data::Validate::Email::is_email($email);

  my $id = $app->add_user($user, $email, $pass);
  $req->session->{user} = $id;

  # A failed default network shouldn't fail the signup.
  eval { add_default_network($app, $id, $user); 1 }
    or warn "Unable to add default network for $user: $@";

  # a streaming response: the server writes it in full when it's passed to
  # $respond, so the email (and any SMTP failure) can't hold up or fail
  # the signup
  my $res = $app->handle("auth.show", $req);
  return sub {
    my $respond = shift;
    $respond->($res);
    send_verify_email($app, $id, $user, $email);
  };
}

sub send_verify_email {
  my ($app, $id, $name, $email) = @_;

  my $token = API::SignedToken->make(
    purpose     => "verify",
    secret      => $app->secret,
    user        => $id,
    fingerprint => $email,
    expires     => time + API::SignedToken::VERIFY_LIFETIME,
  );

  my $data = $tx->render_string($verify_template, {
    name => $name,
    link => $app->site_url . "/verify/#$token",
  });

  return if $ENV{LIERC_NO_SMTP};

  my $msg = MIME::Lite->new(
    From     => $app->from_address,
    To       => $email,
    Subject  => 'Confirm your relaychat.party email address',
    Type     => 'TEXT',
    Encoding => 'quoted-printable',
    Data     => $data,
  );
  eval { send_smtp($app, $msg, $email); 1 }
    or warn "Unable to send verification email for $name: $@";
}

# The configured default network (API::Config default_network), if any,
# with the user's name as nick, username and highlight.
sub add_default_network {
  my ($app, $user_id, $username) = @_;

  my $network = $app->default_network or return;

  my $id = Util->uuid;
  my $nick = Util->irc_nick($username);
  my $config = encode_json {
    %$network,
    Nick      => $nick,
    User      => $nick,
    Highlight => [$nick],
  };

  # Saved before liercd connects, so the connection's first events can be
  # routed to the user. If liercd fails, the stream re-creates the
  # connection from this row when the user next connects.
  $app->save_connection($id, $user_id, $config);
  $app->dbh->do("NOTIFY highlights");

  my $res = $app->request(POST => "$id/create", $config);
  die $res->status_line . "\n" unless $res->code == 200;
}

# MIME::Lite's own SMTP support is broken over TLS: IO::Socket::SSL::start_SSL
# reblesses the socket into Net::SMTP::_SSL, which drops the MIME::Lite::SMTP
# print() override that routes the message through datasend(). The message then
# gets written to the socket raw, with no CRLF line endings and no dot stuffing,
# and the server never sees the end of DATA. Drive Net::SMTP ourselves instead.
sub send_smtp {
  my ($app, $msg, $to) = @_;

  my %opts = %{ $app->smtp_opts };
  my $host = delete $opts{Host};
  my $user = delete $opts{AuthUser};
  my $pass = delete $opts{AuthPass};

  # Net::SMTP's default is 120s; the API has only a few workers, so a dead
  # mail server would tie them all up. SMTP_OPTS can still set its own.
  $opts{Timeout} //= 10;

  my $smtp = Net::SMTP->new((defined $host ? $host : ()), %opts)
    or die "SMTP connect failed: " . ($@ || $!) . "\n";

  if (defined $user) {
    $smtp->auth($user, $pass)
      or die "SMTP AUTH failed: " . $smtp->message;
  }

  $smtp->mail($app->from_address)
    or die "SMTP MAIL FROM failed: " . $smtp->message;
  $smtp->to($to)
    or die "SMTP RCPT TO failed: " . $smtp->message;
  $smtp->data
    or die "SMTP DATA failed: " . $smtp->message;
  $smtp->datasend($msg->as_string)
    or die "SMTP data write failed: " . $smtp->message;
  $smtp->dataend
    or die "SMTP DATAEND failed: " . $smtp->message;

  $smtp->quit;
}

# Confirms the email address in a verification link's token. Doesn't log
# anyone in: links get forwarded.
sub verify {
  my ($app, $req) = @_;
  my $token = $req->parameters->{token};

  my $id = API::SignedToken->user($token);
  my $user = defined $id && $app->dbh->selectrow_hashref(
    q{SELECT id, email FROM "user" WHERE id=?}, {}, $id
  );

  return $app->error($verify_invalid)
    unless $user && API::SignedToken->valid(
      purpose     => "verify",
      secret      => $app->secret,
      token       => $token,
      fingerprint => $user->{email},
      now         => time,
    );

  $app->dbh->do(q{UPDATE "user" SET verified=true WHERE id=?}, {}, $user->{id});
  return $app->ok;
}

# Emails a reset link to the account with this address, if there is one.
# The response is the same either way, and is sent before any email, so the
# time it takes doesn't reveal whether the address has an account.
sub reset {
  my ($app, $req) = @_;
  my $email = $req->parameters->{email};

  die "Invalid email address"
    unless defined $email && Data::Validate::Email::is_email($email);

  my $user = $app->dbh->selectrow_hashref(
    q{SELECT id, username, email, password, password_hash FROM "user" WHERE lower(email) = lower(?)},
    {}, $email
  );

  my $res = $app->ok;
  return $res unless $user;

  # a streaming response: the server writes it in full when it's passed to
  # $respond, and the email goes out after that
  return sub {
    my $respond = shift;
    $respond->($res);
    send_reset_email($app, $user);
  };
}

sub send_reset_email {
  my ($app, $user) = @_;

  my $token = API::SignedToken->make(
    purpose     => "reset",
    secret      => $app->secret,
    user        => $user->{id},
    fingerprint => $user->{password_hash} // $user->{password},
    expires     => time + API::SignedToken::RESET_LIFETIME,
  );

  my $data = $tx->render_string($reset_template, {
    name => $user->{username},
    link => $app->site_url . "/reset/#$token",
  });

  return if $ENV{LIERC_NO_SMTP};

  my $msg = MIME::Lite->new(
    From     => $app->from_address,
    To       => $user->{email},
    Subject  => 'Reset your relaychat.party password',
    Type     => 'TEXT',
    Encoding => 'quoted-printable',
    Data     => $data,
  );
  eval { send_smtp($app, $msg, $user->{email}); 1 }
    or warn "Unable to send reset email for $user->{username}: $@";
}

# Sets a new password from a reset link's token, and logs the user in.
sub reset_confirm {
  my ($app, $req) = @_;
  my $token = $req->parameters->{token};
  my $pass  = $req->parameters->{pass};

  return $app->error("A new password is required")
    unless defined $pass && $pass =~ /\S/;

  my $id = API::SignedToken->user($token);
  my $user = defined $id && $app->dbh->selectrow_hashref(
    q{SELECT id, password, password_hash FROM "user" WHERE id=?}, {}, $id
  );

  return $app->error($reset_expired)
    unless $user && API::SignedToken->valid(
      purpose     => "reset",
      secret      => $app->secret,
      token       => $token,
      fingerprint => $user->{password_hash} // $user->{password},
      now         => time,
    );

  # the new password and verified (the link proves they own the address)
  # change together or not at all. The password only changes if it's still
  # the one the token was checked against, so of two confirms with the same
  # link at once, one wins and the other gets the expired error.
  my $hash = Util->new_password_hash($pass);
  my $dbh = $app->dbh;
  $dbh->begin_work;
  my $changed = eval {
    my $rows = $app->replace_password_hash(
      $user->{id}, $hash, $user->{password_hash}, $user->{password}
    );
    $dbh->do(q{UPDATE "user" SET verified=true WHERE id=?}, {}, $user->{id})
      if $rows;
    $dbh->commit;
    $rows;
  };
  if (my $err = $@) {
    $dbh->rollback;
    die $err;
  }
  return $app->error($reset_expired) unless $changed;

  $req->session->{user} = $user->{id};
  return $app->handle("auth.show", $req);
}

1;
