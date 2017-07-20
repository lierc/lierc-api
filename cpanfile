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
requires "Plack";
requires "Router::Boom";
requires "Plack::Middleware::Session";
requires "Plack::Middleware::ReverseProxy";
requires "Plack::Middleware::Deflater";
requires "DBD::Pg";
requires "IPC::Open3";
requires "Role::Tiny";
requires "Class::Method::Modifiers";
requires "Time::HiRes";
requires "Data::Validate::Email";
requires "Class::Tiny";

# Plack recommends
requires "Cookie::Baker::XS";

# preforking server bin/app.psgi
requires "Gazelle";

# Async stuff bin/events.psgi
requires "AnyEvent::Pg::Pool";
requires "AnyEvent::HTTP";
requires "AnyEvent";
requires "Twiggy";

# AE recommended
requires "EV";
requires "Async::Interrupt";
requires "Guard";
