# ptprint

A Go server for printing labels on a Brother P-touch label printers
(at least the PT-2430PC and PT-P700).

## Prerequisites

* A compatible Brother printer connected by USB to some Linux/Unix
  machine.  ImageMagick's `convert` utility.  (`apt-get install
  imagemagick` on Debian)

## Usage

```
go build
./ptprint.go --dev=/dev/usb/lp1 --port=40404 --convert=/usr/bin/convert   # All flags optional, these are the defaults
```

Then visit `http://your-machine:40404`, enter text, click `Preview`,
once happy, click `Print`.

## udev

This bit is entirely optional, but I've done the following to make the
printer accessible by a normal user.  First, I created
`/etc/udev/rules.d/99-printer.rules`, then I added this content:

```
SUBSYSTEM=="usbmisc", ATTRS{manufacturer}=="Brother", ATTRS{product}=="PT-2430PC", MODE="0666"
```

Swap the value of `ATTRS` for your model. This will make the printer's
device world-writeable, meaning any user can access it.

### Auto-start

Want to get really fancy?  Use the included `ptprint.sh` and
`start_ptprint.sh`. To use them, add
`RUN+="sudo -u <user> /path/to/ptprint/ptprint.sh"` to the
previously-created `99-printer.rules`.  When the printer is connected
or turned on, ptprint will start automatically.

## Weaknesses

* Much of this is guesswork.  As far as I can tell, Brother doesn't
  produce a reference for this printer.
* The (hacky) HTML production is currently one big XSS vulnerability.
  This isn't a problem for me, but might be for you.

## Disclaimer

I am not associated in any way with Brother.

## Author

* [Jon Bright](https://github.com/Jon-Bright)

## License

This project is licensed under the MIT License - see the
[LICENSE.md](LICENSE.md) file for details.
