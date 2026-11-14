/* drumsort: find the drum and percussion one-shots among every sample on an MPC and copy them into one folder,
 * sorted by type (Kicks, Snares, Claps, Hats, Cymbals, Toms, Percs). No basses, chords, melodic sounds or loops.
 *
 * Runs on the MPC itself (MPC TOOLS sends it over and starts it), so the samples never leave the MPC.
 *   drumsort --out "/media/SD/DRUM ONE SHOTS" [--dry] /media/USB1 /media/az01-internal ...
 *
 * How a sound is judged, cheapest first:
 *   1. Its name and the folders above it: "kick", "snare", "hh", "crash", "conga" ... say what it is; "bass", "chord",
 *      "pad", "loop", "piano" ... rule it out ("bass drum" and "drum & bass" don't count as bass).
 *   2. Its length from the file header: one-shots are short (up to 2.5 s; cymbals and open hats up to 8 s).
 *   3. The sound itself (decoded): a fast attack, a level that dies away, how long it holds a clear pitch (a bass or
 *      a melodic sound holds one; a kick only for a moment, usually falling), how long its spectrum stays peaky above
 *      200 Hz (a chord), and how bright it is. A named drum gets more room; an unnamed sound has to look like a drum.
 *   Unnamed sounds that pass go to Kicks (short, low, falling pitch), Hats / Cymbals (bright), or Percs.
 *
 * Copies keep the file as it is (WAV, AIFF, FLAC or MP3), named "<its folder> - <its name>". The same file found twice
 * (identical bytes) is copied once. <out>/.drumsort/index.tsv remembers every file looked at, so running it again only
 * looks at new or changed files. It stops before the output drive has less than 300 MB free.
 *
 * Output lines (for MPC TOOLS): "P|<looked at>|<kept>|<current folder>" for progress, "K|<type>|<count>" per type at the
 * end, "D|<summary>" last; anything else is a message to show.
 *
 * dr_libs (dr_wav, dr_flac, dr_mp3) by David Reid: public domain / MIT-0. This file: MIT, PotentialBreaks. */
#define _GNU_SOURCE
#include <ctype.h>
#include <dirent.h>
#include <errno.h>
#include <math.h>
#include <stdint.h>
#include <stdio.h>
#include <stdlib.h>
#include <string.h>
#include <sys/resource.h>
#include <sys/stat.h>
#include <sys/statvfs.h>
#include <time.h>
#include <unistd.h>

#define DR_WAV_IMPLEMENTATION
#define DR_WAV_NO_STDIO_WIDE
#include "vendor/dr_wav.h"
#define DR_FLAC_IMPLEMENTATION
#define DR_FLAC_NO_WCHAR
#include "vendor/dr_flac.h"
#define DR_MP3_IMPLEMENTATION
#include "vendor/dr_mp3.h"

#ifndef DRUMSORT_VERSION
#define DRUMSORT_VERSION "1.0.0"
#endif

enum { T_NONE = -1, T_KICK, T_SNARE, T_CLAP, T_HAT, T_CYMBAL, T_TOM, T_PERC, NTYPES };
static const char *const TYPE_DIR[NTYPES] = {"Kicks", "Snares", "Claps", "Hats", "Cymbals", "Toms", "Percs"};

#define MAX_DEPTH 12
#define LONG_LIMIT 8.0        /* cymbals and open hats */
#define SHORT_LIMIT 2.5       /* every other named drum */
#define RING_LIMIT 4.0        /* named toms and percussion (congas, tablas ring) */
#define UNNAMED_LIMIT 2.0     /* a sound with no drum word in its name */
#define MIN_FREE_MB 300

/* ---------------------------------------------------------------- words ---------------------------------------------- */
typedef struct { const char *w; int type; } kw_t;
static const kw_t DRUM_WORDS[] = {
    {"kick", T_KICK}, {"kicks", T_KICK}, {"kik", T_KICK}, {"kck", T_KICK}, {"bd", T_KICK}, {"bassdrum", T_KICK},
    {"kickdrum", T_KICK}, {"808bd", T_KICK}, {"909bd", T_KICK},
    {"snare", T_SNARE}, {"snares", T_SNARE}, {"snr", T_SNARE}, {"sd", T_SNARE}, {"sn", T_SNARE}, {"rimshot", T_SNARE},
    {"808sd", T_SNARE}, {"909sd", T_SNARE},
    {"clap", T_CLAP}, {"claps", T_CLAP}, {"clp", T_CLAP}, {"cp", T_CLAP}, {"snap", T_CLAP}, {"snaps", T_CLAP},
    {"fingersnap", T_CLAP},
    {"hat", T_HAT}, {"hats", T_HAT}, {"hihat", T_HAT}, {"hihats", T_HAT}, {"hh", T_HAT}, {"chh", T_HAT}, {"ohh", T_HAT},
    {"oh", T_HAT}, {"ch", T_HAT}, {"openhat", T_HAT}, {"closedhat", T_HAT}, {"808oh", T_HAT}, {"808ch", T_HAT},
    {"808hh", T_HAT},
    {"cymbal", T_CYMBAL}, {"cymbals", T_CYMBAL}, {"cym", T_CYMBAL}, {"crash", T_CYMBAL}, {"crashes", T_CYMBAL},
    {"ride", T_CYMBAL}, {"rides", T_CYMBAL}, {"china", T_CYMBAL}, {"splash", T_CYMBAL}, {"cr", T_CYMBAL},
    {"808cy", T_CYMBAL},
    {"tom", T_TOM}, {"toms", T_TOM}, {"floortom", T_TOM}, {"lt", T_TOM}, {"mt", T_TOM}, {"ht", T_TOM},
    {"perc", T_PERC}, {"percs", T_PERC}, {"percussion", T_PERC}, {"conga", T_PERC}, {"congas", T_PERC},
    {"bongo", T_PERC}, {"bongos", T_PERC}, {"shaker", T_PERC}, {"shakers", T_PERC}, {"shake", T_PERC},
    {"tamb", T_PERC}, {"tambourine", T_PERC}, {"tambo", T_PERC}, {"cowbell", T_PERC}, {"cb", T_PERC},
    {"clave", T_PERC}, {"claves", T_PERC}, {"woodblock", T_PERC}, {"block", T_PERC}, {"rim", T_PERC},
    {"rims", T_PERC}, {"rs", T_PERC}, {"triangle", T_PERC}, {"tabla", T_PERC}, {"cabasa", T_PERC}, {"guiro", T_PERC},
    {"agogo", T_PERC}, {"timbale", T_PERC}, {"timbales", T_PERC}, {"djembe", T_PERC}, {"cajon", T_PERC},
    {"maraca", T_PERC}, {"maracas", T_PERC}, {"stick", T_PERC}, {"sticks", T_PERC}, {"timbal", T_PERC},
    {"surdo", T_PERC}, {"udu", T_PERC}, {"darbuka", T_PERC}, {"doumbek", T_PERC}, {"cuica", T_PERC},
    {"vibraslap", T_PERC}, {"chimes", T_PERC}, {"tok", T_PERC}, {"click", T_PERC}, {"knock", T_PERC},
};
/* words that always rule a sound out: loops, phrases, chords */
static const char *const NEVER[] = {
    "loop", "loops", "bpm", "chord", "chords", "break", "breaks", "breakbeat", "groove", "grooves", "fill", "fills",
    "phrase", "phrases", "song", "songs", "melody", "melodies", "beat", "beats", "top", "tops",
};
/* words that rule a sound out unless its own name also says which drum it is ("Sub Kick", "Bell Ride" stay) */
static const char *const NOT_DRUM[] = {
    "bass", "basses", "bassline", "sub", "subbass", "reese", "keys", "piano",
    "pianos", "rhodes", "wurli", "organ", "organs", "pad", "pads", "synth", "synths", "lead", "leads", "pluck",
    "plucks", "stab", "stabs", "string", "strings", "brass", "horn", "horns", "sax", "trumpet", "trombone", "flute",
    "guitar", "guitars", "gtr", "vocal", "vocals", "vox", "voice", "voices", "choir", "melody", "melodies", "melodic",
    "arp", "arps", "fx", "sfx", "riser", "risers", "sweep", "sweeps", "uplifter", "downlifter", "texture", "textures",
    "ambience", "ambient", "atmos", "atmosphere", "drone", "music", "mallet", "mallets", "marimba", "vibes",
    "xylophone", "glock", "glockenspiel", "harp", "sitar", "koto", "kalimba", "bell", "bells", "note", "notes", "hoover",
    "moog", "juno", "sine", "saw", "square", "speech", "spoken", "talk", "dialog", "dialogue",
};

static int is_drum_neg(const char *const *tok, int n, int i) {  /* "bass" next to "drum": not a bass */
    if (strcmp(tok[i], "bass") && strcmp(tok[i], "basses")) return 0;
    if (i > 0 && (!strcmp(tok[i - 1], "drum") || !strcmp(tok[i - 1], "drums") || !strcmp(tok[i - 1], "and") ||
                  !strcmp(tok[i - 1], "n"))) return 1;
    if (i + 1 < n && (!strcmp(tok[i + 1], "drum") || !strcmp(tok[i + 1], "drums"))) return 1;
    return 0;
}

/* a key name such as "Am", "C#m", "Ebmin", "Fmaj7" on its own: a melodic sound */
static int key_token(const char *t) {
    if (!t[0] || !strchr("abcdefg", t[0])) return 0;
    const char *p = t + 1;
    if (*p == '#' || (*p == 'b' && p[1])) p++;
    if (!strcmp(p, "m") || !strcmp(p, "min") || !strcmp(p, "maj") || !strcmp(p, "minor") || !strcmp(p, "major") ||
        !strcmp(p, "maj7") || !strcmp(p, "m7") || !strcmp(p, "min7") || !strcmp(p, "dim") || !strcmp(p, "sus") ||
        !strcmp(p, "sus2") || !strcmp(p, "sus4"))
        return 1;
    return 0;
}

/* lower-case words of s ("Kick_03 Hard.wav" -> kick, 03, hard; "808BD" -> 808bd, 808, bd) */
#define MAXTOK 48
static int tokens(const char *s, char tok[MAXTOK][32]) {
    int n = 0;
    char cur[32];
    int k = 0;
    for (const char *p = s;; p++) {
        unsigned char c = (unsigned char)*p;
        int al = c && (isalnum(c) || c == '#');
        if (al && k < 31) cur[k++] = (char)tolower(c);
        if ((!al || !c) && k) {
            cur[k] = 0;
            if (n < MAXTOK) snprintf(tok[n++], 32, "%s", cur);
            /* "808bd": also "808" and "bd"; "kick01": also "kick" */
            int d = 0;
            while (cur[d] && isdigit((unsigned char)cur[d])) d++;
            if (d && cur[d] && n < MAXTOK) snprintf(tok[n++], 32, "%s", cur + d);
            int e = k;
            while (e > 0 && isdigit((unsigned char)cur[e - 1])) e--;
            if (e < k && e > 0 && n < MAXTOK) { cur[e] = 0; snprintf(tok[n++], 32, "%s", cur); }
            k = 0;
        }
        if (!c) break;
    }
    return n;
}

typedef struct { int type, never, weak; char why[48]; } words_t;

static int ends_with(const char *t, const char *e) { size_t a = strlen(t), b = strlen(e); return a >= b && !strcmp(t + a - b, e); }
/* a drum word inside a longer word ("popkick", "realclaps", "tn1ride", "brushlotom") */
static int drum_in(const char *t, const char *w) {
    if (!strcmp(t, w)) return 1;
    size_t n = strlen(w);
    if (!strcmp(w, "tom")) return ends_with(t, "tom") && strcmp(t, "bottom") && strcmp(t, "custom") && strcmp(t, "phantom") && strcmp(t, "atom");
    if (!strcmp(w, "ride")) return ends_with(t, "ride") && strcmp(t, "override") && strcmp(t, "pride") && strcmp(t, "bride") && strcmp(t, "stride");
    if (n < 4 || !strcmp(w, "hats") || !strcmp(w, "rims") || !strcmp(w, "rides") || !strcmp(w, "shake") || !strcmp(w, "block") ||
        !strcmp(w, "stick") || !strcmp(w, "click") || !strcmp(w, "snap") || !strcmp(w, "china")) return 0;
    return strstr(t, w) != NULL;
}
/* a "not a drum" word inside a longer word ("jvbass", "padlong"); short or common ones only on their own */
static int weak_in(const char *t, const char *w) {
    if (!strcmp(t, w)) return 1;
    static const char *const ALONE[] = {"note", "notes", "lead", "leads", "sine", "horn", "horns", "harp", "vibes", "koto",
                                        "talk", "bell", "bells", "music", "voice", "string", "organ", "atmos", "keys", "synth"};
    if (strlen(w) < 4) return 0;
    for (size_t i = 0; i < sizeof ALONE / sizeof *ALONE; i++) if (!strcmp(w, ALONE[i])) return 0;
    return strstr(t, w) != NULL;
}

/* looks at one name (a file name without extension, or a folder name); a later name's drum word wins */
static void judge_name(const char *name, words_t *w) {
    char tok[MAXTOK][32];
    const char *tp[MAXTOK];
    int n = tokens(name, tok);
    for (int i = 0; i < n; i++) tp[i] = tok[i];
    for (int i = 0; i < n; i++) {
        for (size_t k = 0; k < sizeof NEVER / sizeof *NEVER; k++)
            if (!strcmp(tp[i], NEVER[k])) { w->never = 1; snprintf(w->why, sizeof w->why, "\"%s\" in the name", NEVER[k]); }
        if (key_token(tp[i])) { w->never = 1; snprintf(w->why, sizeof w->why, "a key (%s) in the name", tp[i]); }
        for (size_t k = 0; k < sizeof NOT_DRUM / sizeof *NOT_DRUM; k++)
            if (weak_in(tp[i], NOT_DRUM[k]) && !is_drum_neg(tp, n, i) && !(strstr(tp[i], "bassdrum") || strstr(tp[i], "drumbass"))) {
                if (!w->weak) snprintf(w->why, sizeof w->why, "\"%s\" in the name", NOT_DRUM[k]);
                w->weak = 1;
            }
        for (size_t k = 0; k < sizeof DRUM_WORDS / sizeof *DRUM_WORDS; k++)
            if (drum_in(tp[i], DRUM_WORDS[k].w)) { w->type = DRUM_WORDS[k].type; break; }
        if (i + 1 < n && !strcmp(tp[i], "bass") && (!strcmp(tp[i + 1], "drum") || !strcmp(tp[i + 1], "drums")))
            w->type = T_KICK;
        if (i + 1 < n && !strcmp(tp[i], "hi") && (!strcmp(tp[i + 1], "hat") || !strcmp(tp[i + 1], "hats")))
            w->type = T_HAT;
    }
}

/* ---------------------------------------------------------------- audio ---------------------------------------------- */
typedef struct { float *x; long n; unsigned rate; } mono_t;

static int ext_is(const char *n, const char *e) {
    size_t a = strlen(n), b = strlen(e);
    return a > b && !strcasecmp(n + a - b, e);
}
static int audio_ext(const char *n) {
    return ext_is(n, ".wav") || ext_is(n, ".aif") || ext_is(n, ".aiff") || ext_is(n, ".flac") || ext_is(n, ".mp3");
}

/* the length in seconds from the header, without decoding; -1 unknown (MP3), -2 unreadable */
static double header_seconds(const char *path) {
    if (ext_is(path, ".mp3")) return -1;
    if (ext_is(path, ".flac")) {
        drflac *f = drflac_open_file(path, NULL);
        if (!f) return -2;
        double s = f->sampleRate ? (double)f->totalPCMFrameCount / f->sampleRate : -2;
        drflac_close(f);
        return s;
    }
    drwav w;
    if (!drwav_init_file(&w, path, NULL)) return -2;
    double s = w.sampleRate ? (double)w.totalPCMFrameCount / w.sampleRate : -2;
    drwav_uninit(&w);
    return s;
}

/* decode at most max_sec seconds, mixed to mono. *more: the file goes on past that */
static int decode_mono(const char *path, double max_sec, mono_t *m, int *more) {
    unsigned ch = 0, rate = 0;
    uint64_t want = 0, got = 0;
    float *raw = NULL;
    *more = 0;
    if (ext_is(path, ".mp3")) {
        drmp3 d;
        if (!drmp3_init_file(&d, path, NULL)) return -1;
        ch = d.channels; rate = d.sampleRate;
        want = (uint64_t)(max_sec * rate) + 1;
        if (ch && rate) raw = malloc((size_t)want * ch * sizeof(float));
        if (raw) got = drmp3_read_pcm_frames_f32(&d, want, raw);
        drmp3_uninit(&d);
        if (got == want) *more = 1;
    } else if (ext_is(path, ".flac")) {
        drflac *f = drflac_open_file(path, NULL);
        if (!f) return -1;
        ch = f->channels; rate = f->sampleRate;
        want = (uint64_t)(max_sec * rate) + 1;
        if (f->totalPCMFrameCount > want) *more = 1;
        if (ch && rate) raw = malloc((size_t)want * ch * sizeof(float));
        if (raw) got = drflac_read_pcm_frames_f32(f, want, raw);
        drflac_close(f);
    } else {
        drwav w;
        if (!drwav_init_file(&w, path, NULL)) return -1;
        ch = w.channels; rate = w.sampleRate;
        want = (uint64_t)(max_sec * rate) + 1;
        if (w.totalPCMFrameCount > want) *more = 1;
        if (ch && rate) raw = malloc((size_t)want * ch * sizeof(float));
        if (raw) got = drwav_read_pcm_frames_f32(&w, want, raw);
        drwav_uninit(&w);
    }
    if (!raw || !got || !rate || rate < 4000 || rate > 384000) { free(raw); return -1; }
    float *x = malloc((size_t)got * sizeof(float));
    if (!x) { free(raw); return -1; }
    for (uint64_t i = 0; i < got; i++) {
        float s = 0;
        for (unsigned c = 0; c < ch; c++) s += raw[i * ch + c];
        x[i] = s / (float)ch;
    }
    free(raw);
    m->x = x; m->n = (long)got; m->rate = rate;
    return 0;
}

/* in-place radix-2 FFT, n a power of two */
static void fft(float *re, float *im, int n) {
    for (int i = 1, j = 0; i < n; i++) {
        int bit = n >> 1;
        for (; j & bit; bit >>= 1) j ^= bit;
        j ^= bit;
        if (i < j) { float t = re[i]; re[i] = re[j]; re[j] = t; t = im[i]; im[i] = im[j]; im[j] = t; }
    }
    for (int len = 2; len <= n; len <<= 1) {
        double a = -2 * M_PI / len;
        float wr = (float)cos(a), wi = (float)sin(a);
        for (int i = 0; i < n; i += len) {
            float cr = 1, ci = 0;
            for (int k = 0; k < len / 2; k++) {
                float ur = re[i + k], ui = im[i + k];
                float vr = re[i + k + len / 2] * cr - im[i + k + len / 2] * ci;
                float vi = re[i + k + len / 2] * ci + im[i + k + len / 2] * cr;
                re[i + k] = ur + vr; im[i + k] = ui + vi;
                re[i + k + len / 2] = ur - vr; im[i + k + len / 2] = ui - vi;
                float t = cr * wr - ci * wi; ci = cr * wi + ci * wr; cr = t;
            }
        }
    }
}

typedef struct {
    double len;          /* seconds until the level falls 30 dB below its loudest */
    double attack;       /* seconds to reach 70% of the loudest */
    int decays;          /* the end is clearly quieter than the start */
    double bright;       /* "RMS frequency" of the first 150 ms after the attack, Hz */
    double tonal;        /* seconds with a clear single pitch */
    double f0_first, f0_last, f0_med;
    double peaky;        /* seconds with a peaky (chord-like) spectrum above 200 Hz */
    double peak;         /* loudest sample */
} feat_t;

static int cmpd(const void *a, const void *b) { double x = *(const double *)a, y = *(const double *)b; return (x > y) - (x < y); }

static void analyse(const mono_t *m, feat_t *f) {
    memset(f, 0, sizeof *f);
    const long N = m->n;
    const double R = m->rate;
    for (long i = 0; i < N; i++) if (fabsf(m->x[i]) > f->peak) f->peak = fabsf(m->x[i]);
    /* level in 5 ms frames */
    long fl = (long)(R * 0.005); if (fl < 1) fl = 1;
    long nf = N / fl;
    if (nf < 2) return;
    double *env = malloc((size_t)nf * sizeof(double)), emax = 0;
    for (long k = 0; k < nf; k++) {
        double s = 0;
        for (long i = k * fl; i < (k + 1) * fl; i++) s += (double)m->x[i] * m->x[i];
        env[k] = sqrt(s / fl);
        if (env[k] > emax) emax = env[k];
    }
    if (emax < 1e-4) { free(env); return; }   /* silence */
    long ka = 0;
    while (ka < nf && env[ka] < 0.7 * emax) ka++;
    long ke = nf - 1;
    while (ke > ka && env[ke] < 0.0316 * emax) ke--;
    f->attack = ka * 0.005;
    f->len = (ke + 1) * 0.005;
    long span = ke - ka + 1;
    if (span >= 8) {
        double a = 0, b = 0;
        long q = span / 4;
        for (long k = ka; k < ka + q; k++) a += env[k];
        for (long k = ke - q + 1; k <= ke; k++) b += env[k];
        f->decays = b < 0.6 * a;
    } else f->decays = 1;
    free(env);

    /* brightness: sqrt(E[d^2]/E[x^2]) * R / (2 pi), over 150 ms from the attack */
    {
        long s0 = (long)(f->attack * R), s1 = s0 + (long)(0.15 * R);
        if (s1 > N) s1 = N;
        double ex = 0, ed = 0;
        for (long i = s0 + 1; i < s1; i++) { double d = m->x[i] - m->x[i - 1]; ed += d * d; ex += (double)m->x[i] * m->x[i]; }
        f->bright = ex > 0 ? sqrt(ed / ex) * R / (2 * M_PI) : 0;
        if (f->bright > R / 2) f->bright = R / 2;
    }

    /* pitch and spectrum at 11025 Hz: average down, frames of 512 (46 ms), hop 256 */
    const double R2 = 11025.0;
    long n2 = (long)(N * R2 / R);
    float *y = malloc((size_t)(n2 > 0 ? n2 : 1) * sizeof(float));
    double ratio = R / R2;
    int box = (int)ceil(ratio); if (box < 1) box = 1;
    for (long j = 0; j < n2; j++) {
        long c = (long)(j * ratio);
        double s = 0; int k = 0;
        for (long i = c; i < c + box && i < N; i++, k++) s += m->x[i];
        y[j] = k ? (float)(s / k) : 0;
    }
    const int W = 512, H = 256, LMIN = 11, LMAX = 276;   /* 40 Hz .. 1000 Hz */
    long j0 = (long)(f->attack * R2), j1 = (long)(f->len * R2);
    if (j1 > n2) j1 = n2;
    double fmax = 0;
    for (long a = j0; a + W <= j1; a += H) {
        double e = 0;
        for (int i = 0; i < W; i++) e += (double)y[a + i] * y[a + i];
        if (e > fmax) fmax = e;
    }
    double f0s[512]; int nf0 = 0;
    float re[512], im[512], hann[512];
    for (int i = 0; i < W; i++) hann[i] = (float)(0.5 - 0.5 * cos(2 * M_PI * i / (W - 1)));
    for (long a = j0; a + W <= j1 && fmax > 0; a += H) {
        double e0 = 0;
        for (int i = 0; i < W; i++) e0 += (double)y[a + i] * y[a + i];
        if (e0 < 0.05 * fmax) continue;   /* -13 dB below the loudest frame: the tail */
        /* clear pitch: normalised autocorrelation */
        double best = 0; int bl = 0;
        for (int l = LMIN; l <= LMAX; l++) {
            double r = 0, ea = 0, eb = 0;
            for (int i = 0; i + l < W; i++) { r += (double)y[a + i] * y[a + i + l]; ea += (double)y[a + i] * y[a + i]; eb += (double)y[a + i + l] * y[a + i + l]; }
            double v = (ea > 0 && eb > 0) ? r / sqrt(ea * eb) : 0;
            if (v > best) { best = v; bl = l; }
        }
        if (best > 0.86 && bl > LMIN) {
            double f0 = R2 / bl;
            if (!nf0) f->f0_first = f0;
            f->f0_last = f0;
            if (nf0 < 512) f0s[nf0++] = f0;
            f->tonal += H / R2;
        }
        /* peaky spectrum above 200 Hz (chords, stabs, keys) */
        for (int i = 0; i < W; i++) { re[i] = y[a + i] * hann[i]; im[i] = 0; }
        fft(re, im, W);
        int b0 = (int)(200.0 * W / R2), b1 = (int)(5000.0 * W / R2);
        double lg = 0, ar = 0, all = 0, hi = 0;
        for (int b = 1; b < W / 2; b++) {
            double p = (double)re[b] * re[b] + (double)im[b] * im[b] + 1e-12;
            all += p;
            if (b >= b0 && b <= b1) { lg += log(p); ar += p; hi += p; }
        }
        int nb = b1 - b0 + 1;
        double flat = ar > 0 ? exp(lg / nb) / (ar / nb) : 1;
        if (flat < 0.02 && hi > 0.3 * all) f->peaky += H / R2;
    }
    if (nf0) { qsort(f0s, (size_t)nf0, sizeof *f0s, cmpd); f->f0_med = f0s[nf0 / 2]; }
    free(y);
}

/* ---------------------------------------------------------------- decision ------------------------------------------- */
typedef struct { int type; const char *why; } verdict_t;

static verdict_t decide(int named, const feat_t *f) {
    verdict_t v = {T_NONE, NULL};
    if (f->peak < 0.003) { v.why = "silent"; return v; }
    if (f->len < 0.02) { v.why = "too short"; return v; }
    if (f->attack > (named != T_NONE ? 0.15 : 0.08)) { v.why = "slow start (a pad, swell or reversed sound)"; return v; }
    if (!f->decays && f->len > 0.35) { v.why = "holds its level (not a hit)"; return v; }
    int falling = f->f0_first > 0 && f->f0_last > 0 && f->f0_first > f->f0_last * 1.12;
    if (named != T_NONE) {
        double lim = named == T_CYMBAL || named == T_HAT ? LONG_LIMIT : named == T_TOM || named == T_PERC ? RING_LIMIT : SHORT_LIMIT;
        if (f->len > lim) { v.why = "too long for a one-shot"; return v; }
        if (named == T_KICK && f->tonal > (falling ? 1.0 : 0.8)) { v.why = "a long held pitch (808 bass?)"; return v; }
        if ((named == T_TOM || named == T_PERC) && f->tonal > 2.0) { v.why = "a long held pitch"; return v; }
        if ((named == T_SNARE || named == T_CLAP || named == T_HAT || named == T_CYMBAL) && f->peaky > 0.6) { v.why = "chord-like"; return v; }
        v.type = named;
        return v;
    }
    /* no drum word: it has to look like a drum */
    if (f->len > UNNAMED_LIMIT) { v.why = "too long for a one-shot"; return v; }
    if (f->peaky > 0.2) { v.why = "chord-like (peaky spectrum)"; return v; }
    int sweep = f->f0_first > 0 && f->f0_last > 0 && f->f0_first > f->f0_last * 1.05;   /* a drum's pitch drops */
    if (f->tonal > 0) {
        /* a steady pitch for most of the sound: a note, not a hit */
        if (f->f0_med >= 120 && (f->tonal > 0.25 || (!sweep && f->tonal >= 0.5 * f->len))) { v.why = "melodic (a clear pitch)"; return v; }
        if (f->tonal > (falling ? 0.6 : 0.3)) { v.why = "a held low pitch (bass)"; return v; }
        if (f->f0_med < 120 && !sweep && f->tonal > 0.1) { v.why = "a low note (bass)"; return v; }
    }
    if (f->tonal > 0.04 && f->f0_med > 0 && f->f0_med < 120 && f->bright < 2500) v.type = T_KICK;
    else if (f->bright > 5000 && f->tonal < 0.05) v.type = f->len < 0.8 ? T_HAT : T_CYMBAL;
    else if (f->bright > 4000 && f->len >= 0.8 && f->tonal < 0.05) v.type = T_CYMBAL;
    else v.type = T_PERC;
    return v;
}

/* ---------------------------------------------------------------- files ---------------------------------------------- */
static uint64_t fnv_file(const char *p, off_t *size) {
    FILE *f = fopen(p, "rb");
    if (!f) return 0;
    uint64_t h = 1469598103934665603ull;
    unsigned char b[65536];
    size_t r;
    off_t n = 0;
    while ((r = fread(b, 1, sizeof b, f)) > 0) {
        for (size_t i = 0; i < r; i++) { h ^= b[i]; h *= 1099511628211ull; }
        n += (off_t)r;
    }
    fclose(f);
    *size = n;
    return h ^ (uint64_t)n;
}

static int copy_to(const char *from, const char *to) {
    FILE *a = fopen(from, "rb");
    if (!a) return -1;
    char tmp[1200];
    snprintf(tmp, sizeof tmp, "%s.part", to);
    FILE *b = fopen(tmp, "wb");
    if (!b) { fclose(a); return -1; }
    char buf[65536];
    size_t r;
    int ok = 1;
    while ((r = fread(buf, 1, sizeof buf, a)) > 0) if (fwrite(buf, 1, r, b) != r) { ok = 0; break; }
    fclose(a);
    if (fclose(b) != 0) ok = 0;
    if (ok && rename(tmp, to) != 0) ok = 0;
    if (!ok) remove(tmp);
    return ok ? 0 : -1;
}

/* FAT/exFAT-safe name: no \ / : * ? " < > |, no control characters, no trailing dots or spaces */
static void safe_name(char *s) {
    for (char *p = s; *p; p++) if (strchr("\\/:*?\"<>|", *p) || (unsigned char)*p < 32) *p = '_';
    size_t n = strlen(s);
    while (n && (s[n - 1] == ' ' || s[n - 1] == '.')) s[--n] = 0;
}

/* ---------------------------------------------------------------- the index ------------------------------------------ */
typedef struct { char *path; long long size, mtime; uint64_t hash; int kept; } rec_t;
static rec_t *idx; static size_t nidx, cidx;
static uint64_t *hashes; static size_t nh, ch_;   /* hashes already copied */
static FILE *idxf;

static int cmp_rec(const void *a, const void *b) { return strcmp(((const rec_t *)a)->path, ((const rec_t *)b)->path); }
static rec_t *find_rec(const char *p) {
    rec_t key = {(char *)p, 0, 0, 0, 0};
    return nidx ? bsearch(&key, idx, nidx, sizeof *idx, cmp_rec) : NULL;
}
static void add_hash(uint64_t h) {
    if (nh == ch_) { ch_ = ch_ ? ch_ * 2 : 1024; hashes = realloc(hashes, ch_ * sizeof *hashes); }
    hashes[nh++] = h;
}
static int have_hash(uint64_t h) { for (size_t i = 0; i < nh; i++) if (hashes[i] == h) return 1; return 0; }

static void load_index(const char *file) {
    FILE *f = fopen(file, "r");
    if (!f) return;
    char line[2400];
    while (fgets(line, sizeof line, f)) {
        line[strcspn(line, "\n")] = 0;
        char *a = strtok(line, "\t"), *b = strtok(NULL, "\t"), *c = strtok(NULL, "\t"), *d = strtok(NULL, "\t"), *e = strtok(NULL, "\t");
        if (!a || !b || !c || !d || !e) continue;
        if (nidx == cidx) { cidx = cidx ? cidx * 2 : 4096; idx = realloc(idx, cidx * sizeof *idx); }
        rec_t *r = &idx[nidx++];
        r->size = atoll(a); r->mtime = atoll(b); r->hash = strtoull(c, NULL, 16); r->kept = atoi(d); r->path = strdup(e);
        if (r->kept && r->hash) add_hash(r->hash);
    }
    fclose(f);
    qsort(idx, nidx, sizeof *idx, cmp_rec);   /* a later line for the same path wins: the sort is stable enough here */
}

/* ---------------------------------------------------------------- the walk ------------------------------------------- */
static char OUT[1024];
static int dry, looked, kept, skipped_known, counts[NTYPES], low_space, verbose;
static time_t last_report;

static int skip_dir(const char *name, const char *full) {
    if (name[0] == '.') return 1;
    static const char *const SKIP[] = {"VST2 Plugins", "System Volume Information", "$RECYCLE.BIN", "lost+found",
                                       "Synths", "Synths-old", "Synths old", "Plugin Skins"};
    for (size_t i = 0; i < sizeof SKIP / sizeof *SKIP; i++) if (!strcasecmp(name, SKIP[i])) return 1;
    size_t lo = strlen(OUT);
    if (!strncmp(full, OUT, lo) && (full[lo] == 0 || full[lo] == '/')) return 1;   /* our own output */
    return 0;
}

static void progress(const char *where) {
    time_t t = time(NULL);
    if (t == last_report) return;
    last_report = t;
    printf("P|%d|%d|%s\n", looked, kept, where);
    fflush(stdout);
}

static int enough_space(void) {
    struct statvfs v;
    if (statvfs(OUT, &v) != 0) return 1;
    return (double)v.f_bavail * v.f_frsize / (1024.0 * 1024.0) > MIN_FREE_MB;
}

/* the folder a copy is named after: the nearest one that says more than "Kicks" or "Samples" (usually the pack) */
static const char *pack_name(const char *const *dirs, int ndirs) {
    static const char *const GENERIC[] = {"drums", "drum", "samples", "sample", "one shots", "oneshots", "one-shots",
                                          "one shot", "sounds", "sound", "wav", "wavs", "audio", "hits", "kit", "kits",
                                          "drum kits", "drum hits", "acoustic", "electronic", "processed", "dry", "wet"};
    for (int i = ndirs - 1; i >= 0; i--) {
        int gen = 0;
        for (size_t k = 0; k < sizeof GENERIC / sizeof *GENERIC; k++) if (!strcasecmp(dirs[i], GENERIC[k])) gen = 1;
        words_t w = {T_NONE, 0, 0, ""};
        judge_name(dirs[i], &w);
        if (!gen && w.type == T_NONE) return dirs[i];
    }
    return ndirs ? dirs[ndirs - 1] : "Sample";
}

static void one_file(const char *path, const char *name, const char *const *dirs, int ndirs, const struct stat *st) {
    looked++;
    rec_t *old = find_rec(path);
    if (old && old->size == (long long)st->st_size && old->mtime == (long long)st->st_mtime) { skipped_known++; return; }

    /* 1. the words */
    words_t wd = {T_NONE, 0, 0, ""}, wf = {T_NONE, 0, 0, ""};
    for (int i = 0; i < ndirs; i++) judge_name(dirs[i], &wd);
    {
        char base[512];
        snprintf(base, sizeof base, "%s", name);
        char *dot = strrchr(base, '.');
        if (dot) *dot = 0;
        judge_name(base, &wf);
    }
    int keep_type = T_NONE;
    const char *why = NULL;
    uint64_t h = 0;
    words_t w = wf;
    if (wf.never) why = wf.why;
    else if (wf.type == T_NONE) {   /* the folders decide */
        w.type = wd.type;
        if (wf.weak) why = wf.why;
        else if (wd.never) why = wd.why;
        else if (wd.weak && wd.type == T_NONE) why = wd.why;
    }
    /* 2. the header */
    double lim = w.type == T_CYMBAL || w.type == T_HAT ? LONG_LIMIT : w.type == T_TOM || w.type == T_PERC ? RING_LIMIT :
                 w.type != T_NONE ? SHORT_LIMIT : UNNAMED_LIMIT;
    if (!why) {
        double hs = header_seconds(path);
        if (hs == -2) why = "can't be read";
        else if (hs > lim + 3.0) why = "too long for a one-shot";
    }
    /* 3. the sound */
    if (!why) {
        mono_t m = {0};
        int more = 0;
        if (decode_mono(path, lim + 3.0, &m, &more) != 0) why = "can't be read";
        else if (more) why = "too long for a one-shot";
        else {
            feat_t f;
            analyse(&m, &f);
            verdict_t v = decide(w.type, &f);
            keep_type = v.type; why = v.why;
            if (verbose)
                fprintf(stderr, "%s\t%s\tlen=%.2f att=%.3f dec=%d bright=%.0f tonal=%.2f f0=%.0f/%.0f/%.0f peaky=%.2f\t%s\n",
                        keep_type >= 0 ? TYPE_DIR[keep_type] : "-", path, f.len, f.attack, f.decays, f.bright, f.tonal,
                        f.f0_first, f.f0_med, f.f0_last, f.peaky, why ? why : "");
        }
        free(m.x);
    } else if (verbose) fprintf(stderr, "-\t%s\t\t%s\n", path, why);

    int copied = 0;
    if (keep_type != T_NONE) {
        off_t sz = 0;
        h = fnv_file(path, &sz);
        if (have_hash(h)) keep_type = T_NONE;   /* the same file was copied already */
        else if (!dry) {
            if (!enough_space()) { low_space = 1; return; }
            char dir[1100], nm[600], dest[1800];
            snprintf(dir, sizeof dir, "%s/%s", OUT, TYPE_DIR[keep_type]);
            mkdir(dir, 0777);
            snprintf(nm, sizeof nm, "%s - %s", pack_name(dirs, ndirs), name);
            safe_name(nm);
            if (strlen(nm) > 120) {   /* keep the extension */
                const char *e = strrchr(nm, '.');
                char ext[16] = "";
                if (e && strlen(e) < sizeof ext) snprintf(ext, sizeof ext, "%s", e);
                nm[120 - strlen(ext)] = 0;
                strcat(nm, ext);
            }
            snprintf(dest, sizeof dest, "%s/%s", dir, nm);
            struct stat ds;
            for (int k = 2; stat(dest, &ds) == 0 && k < 1000; k++) {
                char stem[600], ext[16] = "";
                snprintf(stem, sizeof stem, "%s", nm);
                char *e = strrchr(stem, '.');
                if (e && strlen(e) < sizeof ext) { snprintf(ext, sizeof ext, "%s", e); *e = 0; }
                snprintf(dest, sizeof dest, "%s/%s (%d)%s", dir, stem, k, ext);
            }
            if (copy_to(path, dest) == 0) copied = 1;
            else { printf("Couldn't copy %s\n", path); keep_type = T_NONE; }
        } else copied = 1;
        if (copied) { add_hash(h); kept++; counts[keep_type]++; }
    }
    if (idxf && !dry) {
        fprintf(idxf, "%lld\t%lld\t%llx\t%d\t%s\n", (long long)st->st_size, (long long)st->st_mtime,
                (unsigned long long)h, copied, path);
        fflush(idxf);
    }
}

static void walk(const char *dir, const char **dirs, int ndirs) {
    if (ndirs >= MAX_DEPTH || low_space) return;
    DIR *d = opendir(dir);
    if (!d) return;
    struct dirent *e;
    char **subs = NULL; size_t ns = 0, cs = 0;
    while ((e = readdir(d)) && !low_space) {
        if (e->d_name[0] == '.') continue;
        char p[2048];
        snprintf(p, sizeof p, "%s/%s", dir, e->d_name);
        struct stat st;
        if (lstat(p, &st) != 0 || S_ISLNK(st.st_mode)) continue;
        if (S_ISDIR(st.st_mode)) {
            if (skip_dir(e->d_name, p)) continue;
            if (ns == cs) { cs = cs ? cs * 2 : 16; subs = realloc(subs, cs * sizeof *subs); }
            subs[ns++] = strdup(e->d_name);
        } else if (S_ISREG(st.st_mode) && audio_ext(e->d_name) && st.st_size > 200) {
            progress(dir);
            one_file(p, e->d_name, dirs, ndirs, &st);
        }
    }
    closedir(d);
    for (size_t i = 0; i < ns; i++) {
        char p[2048];
        snprintf(p, sizeof p, "%s/%s", dir, subs[i]);
        dirs[ndirs] = subs[i];
        walk(p, dirs, ndirs + 1);
        free(subs[i]);
    }
    free(subs);
}

int main(int argc, char **argv) {
    const char *roots[64]; int nr = 0;
    for (int i = 1; i < argc; i++) {
        if (!strcmp(argv[i], "--out") && i + 1 < argc) snprintf(OUT, sizeof OUT, "%s", argv[++i]);
        else if (!strcmp(argv[i], "--dry")) dry = 1;
        else if (!strcmp(argv[i], "-v")) verbose = 1;
        else if (!strcmp(argv[i], "--version")) { printf("drumsort %s\n", DRUMSORT_VERSION); return 0; }
        else if (nr < 64) roots[nr++] = argv[i];
    }
    if (!OUT[0] || !nr) { fprintf(stderr, "usage: drumsort --out <folder> [--dry] [-v] <folder>...\n"); return 2; }
    while (strlen(OUT) > 1 && OUT[strlen(OUT) - 1] == '/') OUT[strlen(OUT) - 1] = 0;
    setpriority(PRIO_PROCESS, 0, 19);   /* the MPC app comes first */
    char meta[1100], ix[1200];
    snprintf(meta, sizeof meta, "%s/.drumsort", OUT);
    snprintf(ix, sizeof ix, "%s/index.tsv", meta);
    if (!dry) {
        if (mkdir(OUT, 0777) != 0 && errno != EEXIST) { printf("Couldn't make %s (%s)\n", OUT, strerror(errno)); return 1; }
        mkdir(meta, 0777);
        load_index(ix);
        idxf = fopen(ix, "a");
        if (!idxf) { printf("Couldn't write in %s (%s)\n", OUT, strerror(errno)); return 1; }
        if (!enough_space()) { printf("The drive with %s has less than %d MB free.\n", OUT, MIN_FREE_MB); return 1; }
    }
    if (nidx) printf("Looked at %zu files before: only new or changed ones are checked.\n", nidx);
    fflush(stdout);
    for (int i = 0; i < nr && !low_space; i++) {
        printf("Looking in %s\n", roots[i]);
        fflush(stdout);
        const char *dirs[MAX_DEPTH + 1];
        walk(roots[i], dirs, 0);
    }
    if (idxf) fclose(idxf);
    for (int t = 0; t < NTYPES; t++) printf("K|%s|%d\n", TYPE_DIR[t], counts[t]);
    if (low_space) printf("Stopped: the drive is nearly full (less than %d MB free).\n", MIN_FREE_MB);
    printf("D|Looked at %d sound files (%d already known), kept %d new drum one-shots.\n", looked, skipped_known, kept);
    return 0;
}
