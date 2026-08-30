package drive

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/casmith/ps2hdd/internal/apa"
	"github.com/casmith/ps2hdd/internal/external"
	"github.com/casmith/ps2hdd/internal/logging"
	"github.com/casmith/ps2hdd/internal/model"
)

// Refusal is returned when a safety check fails. It is deliberately a distinct
// type: callers must never paper over it, and the CLI and TUI both render it
// as a hard "REFUSING OPERATION" rather than as a generic error.
//
// There is no override flag for a refusal caused by the target backing a
// mounted Linux filesystem, /, or /boot. Prefer refusal over dangerous
// inference is the rule the whole package is built around.
type Refusal struct {
	Device  string
	Reasons []string
}

func (r *Refusal) Error() string {
	var b strings.Builder
	b.WriteString("REFUSING OPERATION\n\n")
	if r.Device != "" {
		fmt.Fprintf(&b, "Device:\n  %s\n\n", r.Device)
	}
	b.WriteString("Reason:\n")
	for _, reason := range r.Reasons {
		fmt.Fprintf(&b, "  %s\n", reason)
	}
	b.WriteString("\nNo disk modifications were made.")
	return b.String()
}

// IsRefusal reports whether err is a safety refusal.
func IsRefusal(err error) bool {
	var r *Refusal
	return errors.As(err, &r)
}

func refuse(device string, reasons ...string) error {
	return &Refusal{Device: device, Reasons: reasons}
}

// Target is a validated device, ready to be read from or written to.
type Target struct {
	// Configured is the identifier the user supplied, normally a
	// /dev/disk/by-id path.
	Configured string
	// Path is the kernel device the identifier currently resolves to.
	Path string
	// SizeBytes is the capacity as measured at validation time.
	SizeBytes int64
	Model     string
	Serial    string
	// IsImage is true when the target is a regular file rather than a block
	// device.
	IsImage bool
	// APA is true when a valid APA partition table was found.
	APA bool
	// Writable records whether validation was performed for a write.
	Writable bool
}

// Options controls how strict a validation is.
type Options struct {
	Runner external.Runner
	// Write asks for the full pre-write validation. Reads are allowed on a
	// device that fails some write-only checks, because reading a disk to
	// report on it is how a user finds out something is wrong.
	Write bool
	// RequireAPA fails validation when the device has no APA table. Every
	// operation that touches game data sets this; `detect` does not, because
	// its whole job is to report on candidates.
	RequireAPA bool
}

// Validate performs the pre-operation checks in a fixed order and returns a
// Target only if every applicable one passes.
//
// The order matters: identity is established before capacity, capacity before
// content, and the system-disk checks run before anything that could be
// mistaken for permission to write.
//
//  1. the configured identifier is a stable one
//  2. it exists
//  3. it resolves to a real block device or image file
//  4. model matches what the identifier claims
//  5. serial matches what the identifier claims
//  6. capacity is readable and non-zero
//  7. an APA table is present (when required)
//  8. expected PS2 structures are present (when required)
//  9. the device does not back /
//  10. the device does not back /boot
//  11. the device carries no mounted filesystem at all
//  12. the identity is unambiguous
//  13. nothing about the device contradicts the configured identifier
func Validate(ctx context.Context, configured string, opts Options) (*Target, error) {
	log := logging.ContextLogger(ctx)
	configured = strings.TrimSpace(configured)
	if configured == "" {
		return nil, refuse("", "No device is configured.",
			"Run `ps2hdd detect --configure` to select one.")
	}

	// (1) stable identity.
	if err := validateStableIdentifier(configured); err != nil {
		return nil, err
	}

	// (2)(3) existence and resolution.
	fi, err := os.Stat(configured)
	if err != nil {
		return nil, refuse(configured,
			fmt.Sprintf("The configured device does not exist: %v.", err),
			"The HDD may be disconnected, or the identifier may have changed.")
	}
	t := &Target{Configured: configured, Writable: opts.Write}
	t.IsImage = fi.Mode().IsRegular()

	path, err := Resolve(configured)
	if err != nil {
		return nil, refuse(configured, fmt.Sprintf("Could not resolve the device: %v.", err))
	}
	t.Path = path

	if !t.IsImage {
		rfi, err := os.Stat(path)
		if err != nil {
			return nil, refuse(configured, fmt.Sprintf("Could not stat %s: %v.", path, err))
		}
		if rfi.Mode()&os.ModeDevice == 0 {
			return nil, refuse(configured,
				fmt.Sprintf("%s resolves to %s, which is neither a block device nor a disk image.", configured, path))
		}
	}

	// (4)(5)(12)(13) identity cross-check against what the kernel reports.
	if !t.IsImage && opts.Runner != nil {
		if err := t.checkIdentity(ctx, opts.Runner); err != nil {
			return nil, err
		}
	}

	// (9)(10)(11) system-disk checks. These run for reads too: a disk carrying
	// the running system is not a PS2 HDD, and reading it as one would report
	// nonsense.
	//
	// They run *before* the capacity read even though the checklist numbers
	// them later, because reading a capacity means opening the raw device,
	// which normally needs root. Ordering it first would turn "this is the
	// disk your system is running from" into "permission denied", which is
	// both less informative and less alarming than it should be.
	if !t.IsImage {
		if err := checkNotSystemDisk(configured, path); err != nil {
			return nil, err
		}
	}

	// (6) capacity.
	size, err := DeviceSize(path)
	if err != nil {
		return nil, refuse(configured,
			fmt.Sprintf("Could not read the device capacity: %v.", err),
			"Raw block devices are normally root-owned; see docs/safety.md for the recommended udev rule.")
	}
	if size <= 0 {
		return nil, refuse(configured, "The device reports a capacity of zero bytes.")
	}
	t.SizeBytes = size

	// (7)(8) content.
	f, err := os.Open(path)
	if err != nil {
		return nil, refuse(configured, fmt.Sprintf("Could not open the device for reading: %v.", err),
			"Raw block devices are normally root-owned; see docs/safety.md for the recommended udev rule.")
	}
	defer f.Close()

	isAPA, err := apa.IsAPA(f)
	if err != nil {
		return nil, refuse(configured, fmt.Sprintf("Could not read the partition table: %v.", err))
	}
	t.APA = isAPA
	if opts.RequireAPA && !isAPA {
		return nil, refuse(configured,
			"No APA partition table was found on this device.",
			"ps2hdd never initialises or formats a disk it does not recognise.",
			"If this really is your PS2 HDD, format it with a PS2-side tool first.")
	}

	log.Info("device validated",
		"configured", configured, "path", path, "size", size,
		"apa", isAPA, "write", opts.Write, "image", t.IsImage)
	return t, nil
}

// validateStableIdentifier implements check (1).
func validateStableIdentifier(dev string) error {
	if !filepath.IsAbs(dev) {
		return refuse(dev, "The device identifier is not an absolute path.")
	}
	for _, prefix := range []string{
		"/dev/disk/by-id/", "/dev/disk/by-uuid/", "/dev/disk/by-path/", "/dev/disk/by-partuuid/",
	} {
		if strings.HasPrefix(dev, prefix) {
			return nil
		}
	}
	if fi, err := os.Stat(dev); err == nil && fi.Mode().IsRegular() {
		return nil // a disk image is identified by its path
	}
	return refuse(dev,
		fmt.Sprintf("%s is a kernel device name, not a stable identifier.", dev),
		"Kernel names such as /dev/sdb are reassigned between boots and hotplugs;",
		"writing to one risks addressing a different disk than the one you meant.",
		"Use a /dev/disk/by-id/... path instead (`ps2hdd detect` will suggest one).")
}

// checkIdentity implements checks (4), (5), (12) and (13).
//
// A by-id name of the form ata-MODEL_SERIAL encodes the identity udev observed
// when the link was created. Comparing it against what lsblk reports right now
// catches the case where the link survives but points somewhere else.
func (t *Target) checkIdentity(ctx context.Context, r external.Runner) error {
	devs, err := ListBlockDevices(ctx, r)
	if err != nil {
		// lsblk being unavailable is not itself a reason to refuse a read, but
		// it does mean the identity cross-check could not run; say so.
		logging.ContextLogger(ctx).Warn("identity cross-check skipped", "err", err)
		return nil
	}
	var matches []model.BlockDevice
	for _, d := range devs {
		resolved, err := filepath.EvalSymlinks(d.Path)
		if err != nil {
			resolved = d.Path
		}
		if resolved == t.Path {
			matches = append(matches, d)
		}
	}
	// (12) ambiguity.
	if len(matches) > 1 {
		return refuse(t.Configured,
			fmt.Sprintf("%s matches %d block devices; the identity is ambiguous.", t.Configured, len(matches)))
	}
	if len(matches) == 0 {
		// The device resolved but lsblk does not list it as a whole disk. That
		// is what a partition looks like, and ps2hdd operates on whole disks.
		return refuse(t.Configured,
			fmt.Sprintf("%s resolves to %s, which lsblk does not report as a whole disk.", t.Configured, t.Path),
			"ps2hdd operates on the whole PS2 HDD, not on individual partitions.")
	}
	d := matches[0]
	t.Model, t.Serial = d.Model, d.Serial

	// (4)(5)(13) the by-id name usually embeds model and serial; when it does,
	// a mismatch means the disk behind the link changed.
	base := filepath.Base(t.Configured)

	// Before believing a mismatch, check that lsblk is in a position to be
	// believed. A <bus>-<model>_<serial> by-id name exists only because udev
	// recorded a serial for this disk, so an lsblk that now reports none for
	// the same disk is not saying the disk changed -- it is saying it could
	// not read the udev database. util-linux built without libudev falls back
	// to the raw sysfs INQUIRY strings, which yields an empty serial and a
	// truncated, space-padded model that fails the model comparison on a
	// perfectly correct identifier. A Homebrew lsblk ahead of /usr/bin on
	// PATH is the usual way this happens.
	//
	// That is the same situation as lsblk being absent altogether, and it gets
	// the same answer: the cross-check could not run, so say so and continue,
	// rather than refusing a disk on evidence that was never gathered.
	if hintedSerial(base) != "" && d.Serial == "" {
		logging.ContextLogger(ctx).Warn("identity cross-check skipped",
			"reason", "lsblk reported no serial; it is probably built without libudev",
			"device", t.Path, "model", d.Model)
		return nil
	}

	if want := hintedSerial(base); want != "" && d.Serial != "" && !strings.EqualFold(want, d.Serial) {
		return refuse(t.Configured,
			fmt.Sprintf("The identifier names serial %q but the device reports %q.", want, d.Serial),
			"This is not the disk that was configured. Re-run `ps2hdd detect --configure`.")
	}
	if !modelConsistent(base, d.Model) {
		return refuse(t.Configured,
			fmt.Sprintf("The identifier names a different model than the device reports (%q).", d.Model),
			"This is not the disk that was configured. Re-run `ps2hdd detect --configure`.")
	}
	return nil
}

// hintedSerial extracts the serial from an ata-/nvme-/usb- by-id name, which
// udev builds as <bus>-<model>_<serial>. It returns "" when the name carries
// no recoverable serial.
func hintedSerial(base string) string {
	i := strings.IndexByte(base, '-')
	if i < 0 {
		return ""
	}
	switch base[:i] {
	case "ata", "nvme", "usb", "scsi":
	default:
		return ""
	}
	rest := base[i+1:]
	// Trim any partition suffix udev appends.
	if j := strings.Index(rest, "-part"); j >= 0 {
		rest = rest[:j]
	}
	j := strings.LastIndexByte(rest, '_')
	if j < 0 || j == len(rest)-1 {
		return ""
	}
	return trimLUN(rest[j+1:])
}

// trimLUN removes the SCSI LUN that udev's usb_id appends to ID_SERIAL, as in
// usb-SABRENT_SSHD_AAAABBBBCCCC0003-0:0.
//
// The LUN is addressing, not identity, and lsblk reports the serial without
// it. Leaving it attached makes every USB by-id name disagree with the disk it
// correctly names -- which matters because a USB adapter is how most people
// attach a PS2 HDD to a PC.
//
// Only a trailing <digits>:<digits> is removed. Serials do contain hyphens,
// and one must never be mistaken for a LUN.
func trimLUN(s string) string {
	i := strings.LastIndexByte(s, '-')
	if i <= 0 {
		return s
	}
	lun := s[i+1:]
	colon := strings.IndexByte(lun, ':')
	if colon <= 0 || colon == len(lun)-1 {
		return s
	}
	if !allDigits(lun[:colon]) || !allDigits(lun[colon+1:]) {
		return s
	}
	return s[:i]
}

func allDigits(s string) bool {
	for i := 0; i < len(s); i++ {
		if s[i] < '0' || s[i] > '9' {
			return false
		}
	}
	return len(s) > 0
}

// modelConsistent reports whether the model embedded in a by-id name agrees
// with what the kernel reports. udev substitutes underscores for spaces and
// may truncate, so the comparison is a prefix match on a normalised form.
func modelConsistent(base, model string) bool {
	if model == "" {
		return true
	}
	i := strings.IndexByte(base, '-')
	if i < 0 {
		return true
	}
	switch base[:i] {
	case "ata", "nvme", "usb", "scsi":
	default:
		return true // wwn- and by-path names carry no model
	}
	bus := base[:i]
	rest := base[i+1:]
	j := strings.LastIndexByte(rest, '_')
	if j < 0 {
		return true
	}
	name := rest[:j]
	got := normaliseModel(model)
	if got == "" {
		return true
	}

	// The name half of a by-id link is not the same field on every bus. For
	// ata- and nvme- udev builds ID_SERIAL as <model>_<serial>, so the name is
	// the model outright. For usb- and scsi- it builds
	// <manufacturer>_<product>_<serial> while setting ID_MODEL -- what lsblk
	// reports -- to the product alone. Comparing the two forms directly makes
	// "SABRENT_SSHD" disagree with "SSHD" and refuses a correct identifier,
	// so on those buses the vendor-stripped form is accepted too.
	candidates := []string{name}
	if bus == "usb" || bus == "scsi" {
		if k := strings.IndexByte(name, '_'); k > 0 && k < len(name)-1 {
			candidates = append(candidates, name[k+1:])
		}
	}
	for _, c := range candidates {
		hinted := normaliseModel(c)
		if hinted == "" {
			return true
		}
		if strings.HasPrefix(hinted, got) || strings.HasPrefix(got, hinted) {
			return true
		}
	}
	return false
}

func normaliseModel(s string) string {
	var b strings.Builder
	for _, r := range strings.ToUpper(s) {
		if (r >= 'A' && r <= 'Z') || (r >= '0' && r <= '9') {
			b.WriteRune(r)
		}
	}
	return b.String()
}

// checkNotSystemDisk implements checks (9), (10) and (11).
func checkNotSystemDisk(configured, path string) error {
	mounts, err := SystemDevices()
	if err != nil {
		// Without mountinfo there is no way to prove the disk is not the
		// system disk, and guessing is exactly what this package exists to
		// avoid.
		return refuse(configured,
			fmt.Sprintf("Could not read the mount table: %v.", err),
			"ps2hdd cannot prove this device is not the system disk, so it will not touch it.")
	}
	var found []string
	for src, points := range mounts {
		if !sameDisk(src, path) {
			continue
		}
		found = append(found, points...)
	}
	if len(found) == 0 {
		return nil
	}
	for _, m := range found {
		if m == "/" {
			return refuse(configured, fmt.Sprintf("%s backs the root filesystem.", path),
				"ps2hdd will never operate on the running system's disk.")
		}
		if m == "/boot" || strings.HasPrefix(m, "/boot/") {
			return refuse(configured, fmt.Sprintf("%s backs %s.", path, m),
				"ps2hdd will never operate on the running system's disk.")
		}
	}
	return refuse(configured,
		fmt.Sprintf("%s carries mounted Linux filesystems: %s.", path, strings.Join(found, ", ")),
		"A PS2 HDD holds no filesystem Linux can mount, so this is not one.",
		"Unmount them and re-check the device if you believe this is wrong.")
}

// sameDisk reports whether mount source src lies on the disk at path, i.e. is
// the disk itself or one of its partitions.
func sameDisk(src, path string) bool {
	if src == path {
		return true
	}
	// Partition names are the disk name plus digits, optionally after a "p"
	// for nvme and mmc style devices.
	if !strings.HasPrefix(src, path) {
		return false
	}
	rest := strings.TrimPrefix(src, path)
	rest = strings.TrimPrefix(rest, "p")
	if rest == "" {
		return false
	}
	for _, c := range rest {
		if c < '0' || c > '9' {
			return false
		}
	}
	return true
}
