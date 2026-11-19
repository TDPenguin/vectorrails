# vectorrails

A small local proxy to bridge OpenRailwayMap (ORM) vector tiles with NIMBY Rails.

To be used with ***mods I am not done with yet.***

## Requirements

- Go (any recent version)

## Build and run

```
go build -o vectorrails .
./vectorrails
```

The proxy listens on `127.0.0.1:8080` and forwards to `https://openrailwaymap.app`.

## Use it in the game

In your map style mod, point the tile source at the proxy instead of ORM:

```
http://localhost:8080/<layer>/{z}/{x}/{y}
```

To use several ORM layers in one source, join the layer names with commas:

```
http://localhost:8080/railway_line_high,railway_text_km/{z}/{x}/{y}
```

Some layers may not be valid, check the network inspection logs on OpenRailwayMap.app to see valid combinations.

## Options

| Flag | Default | Purpose |
| --- | --- | --- |
| `-listen` | `127.0.0.1:8080` | Address the proxy listens on |
| `-upstream` | `https://openrailwaymap.app` | Tile server to fetch from, no trailing slash |
| `-fixbools` | `true` | Rewrite true/false values as text. Set `-fixbools=false` to pass tiles through untouched |

Example, using a self-hosted ORM server:

```
./vectorrails -upstream http://localhost:8000
```

## Using the public ORM server

The public server is a shared service. This proxy sends requests as itself and does not pretend to be a browser or another app. If you use it heavily, consider running your own ORM instance and pointing `-upstream` at it.