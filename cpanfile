# Main app
requires "Encode";
requires "LWP::UserAgent";
requires "Data::UUID";
requires "URL::Encode";
requires "JSON::XS";
requires "List::Util";
requires "Math::BaseConvert";
requires "DBIx::Connector";
requires "Digest";
requires "Digest::Bcrypt";
requires "Twiggy";
requires "Plack::Builder";
requires "Router::Boom";
requires "Plack::App::File";
requires "Plack::Middleware::Session";
requires "Plack::Middleware::ReverseProxy";
requires "DBD::Pg";
requires "IPC::Open3";
requires "Role::Tiny";
requires "Time::HiRes";
requires "Data::Validate::Email";

# Async stuff bin/events.psgi
requires "AnyEvent::DBI";
requires "AnyEvent::HTTP";
requires "AnyEvent";

# preforking server bin/app.psgi
requires "Gazelle";

# AE recommended
requires "EV";
requires "Async::Interrupt";
requires "Guard";
requires "AnyEvent::AIO";
