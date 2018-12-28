# ptprint

A Go server for printing labels on a Brother P-touch PT-2430PC label printer.

## Prerequisites

* A PT-2430PC printer connected by USB to some Linux/Unix machine.
* ImageMagick's `convert` utility.  (`apt-get install imagemagick` on Debian)

## Usage

```
go build ptprint.go
./ptprint.go --dev=/dev/usb/lp1 --port=40404 --convert=/usr/bin/convert   # All flags optional, these are the defaults
```

Then visit `http://your-machine:40404`, enter text, click `Preview`, once happy, click `Print`.

## Weaknesses

* Much of this is guesswork.  As far as I can tell, Brother doesn't produce a reference for this printer.
* The (hacky) HTML production is currently one big XSS vulnerability.  This isn't a problem for me, but might be for you.

## Disclaimer

I am not associated in any way with Brother.

## Author

* [Jon Bright](https://github.com/Jon-Bright)

## License

This project is licensed under the MIT License - see the [LICENSE.md](LICENSE.md) file for details.
