/* linkaudio-rx: receives one Ableton Link Audio channel and writes it to stdout for MPC TOOLS.
 *
 * Copyright 2026 PotentialBreaks
 *
 * This program is free software: you can redistribute it and/or modify it under the terms of
 * the GNU General Public License as published by the Free Software Foundation, either version 2
 * of the License, or (at your option) any later version. It is built with the Ableton Link SDK
 * (https://github.com/Ableton/link), which is under the same licence. See README.md here.
 *
 * MPC TOOLS runs this as a separate program and talks to it through pipes:
 *   stdin  (text lines)   "sub <16 hex digits>" receive that channel, "off" stop receiving,
 *                         "quit" exit. Closing stdin also exits, so it never outlives MPC TOOLS.
 *   stderr (text lines)   "peers <n>"; "chs" then one "ch <id>\t<peer name>\t<channel name>" line
 *                         per channel then "end", whenever the list changes; "sub <id>"; "off".
 *   stdout (binary)       one record per received buffer, a 16-byte header then the samples:
 *                         'L' 'A' channels(u8) 0 | frames(u16) 0 0 | rate(u32) | count(u32),
 *                         little-endian, then frames*channels int16 samples, interleaved.
 */

#include <ableton/LinkAudio.hpp>

#include <atomic>
#include <chrono>
#include <condition_variable>
#include <cstdint>
#include <cstdio>
#include <deque>
#include <iostream>
#include <memory>
#include <mutex>
#include <optional>
#include <sstream>
#include <string>
#include <thread>
#include <vector>

#if defined(_WIN32)
#include <fcntl.h>
#include <io.h>
#endif

namespace
{

std::mutex gErrMutex;

void say(const std::string& line)
{
  std::lock_guard<std::mutex> lock(gErrMutex);
  std::fputs((line + "\n").c_str(), stderr);
  std::fflush(stderr);
}

std::string toHex(const ableton::ChannelId& id)
{
  static const char* digits = "0123456789abcdef";
  std::string s;
  for (auto b : id)
  {
    s += digits[b >> 4];
    s += digits[b & 15];
  }
  return s;
}

std::optional<ableton::ChannelId> fromHex(const std::string& s)
{
  ableton::ChannelId id{};
  if (s.size() != id.size() * 2)
    return std::nullopt;
  for (std::size_t i = 0; i < id.size(); ++i)
  {
    unsigned v = 0;
    if (std::sscanf(s.c_str() + i * 2, "%2x", &v) != 1)
      return std::nullopt;
    id[i] = static_cast<std::uint8_t>(v);
  }
  return id;
}

std::string clean(std::string s) // names go on tab-separated lines
{
  for (auto& c : s)
    if (c == '\t' || c == '\n' || c == '\r')
      c = ' ';
  return s;
}

// Buffers are handed from the Link thread to a writer thread, so a slow reader never holds up
// Link. If MPC TOOLS falls far behind, the oldest buffers are dropped.
class Writer
{
public:
  Writer()
    : mThread([this] { run(); })
  {
  }

  ~Writer()
  {
    {
      std::lock_guard<std::mutex> lock(mMutex);
      mStop = true;
    }
    mCv.notify_one();
    mThread.join();
  }

  void push(const ableton::LinkAudioSource::BufferHandle& h)
  {
    const auto ch = h.info.numChannels, fr = h.info.numFrames;
    if (ch == 0 || ch > 255 || fr == 0 || fr > 65535)
      return;
    std::vector<std::uint8_t> rec;
    header(rec, ch, fr, h.info.sampleRate, static_cast<std::uint32_t>(h.info.count));
    for (std::size_t i = 0; i < ch * fr; ++i)
    {
      const auto v = static_cast<std::uint16_t>(h.samples[i]);
      rec[16 + i * 2] = static_cast<std::uint8_t>(v & 0xff);
      rec[16 + i * 2 + 1] = static_cast<std::uint8_t>(v >> 8);
    }
    {
      std::lock_guard<std::mutex> lock(mMutex);
      mQueue.push_back(std::move(rec));
      while (mQueue.size() > 400)
        mQueue.pop_front();
    }
    mCv.notify_one();
  }

private:
  static void put16(std::vector<std::uint8_t>& b, std::size_t at, std::uint16_t v)
  {
    b[at] = static_cast<std::uint8_t>(v & 0xff);
    b[at + 1] = static_cast<std::uint8_t>(v >> 8);
  }

  static void put32(std::vector<std::uint8_t>& b, std::size_t at, std::uint32_t v)
  {
    for (int i = 0; i < 4; ++i)
      b[at + i] = static_cast<std::uint8_t>((v >> (8 * i)) & 0xff);
  }

  // The record header, 16 bytes: 'L' 'A' channels 0 | frames(u16) 0 0 | rate(u32) | count(u32)
  static void header(std::vector<std::uint8_t>& b,
                  std::size_t ch,
                  std::size_t fr,
                  std::uint32_t rate,
                  std::uint32_t count)
  {
    b.resize(16 + ch * fr * 2);
    b[0] = 'L';
    b[1] = 'A';
    b[2] = static_cast<std::uint8_t>(ch);
    b[3] = 0;
    put16(b, 4, static_cast<std::uint16_t>(fr));
    b[6] = 0;
    b[7] = 0;
    put32(b, 8, rate);
    put32(b, 12, count);
  }

  void run()
  {
    for (;;)
    {
      std::vector<std::uint8_t> rec;
      {
        std::unique_lock<std::mutex> lock(mMutex);
        mCv.wait(lock, [this] { return mStop || !mQueue.empty(); });
        if (mStop)
          return;
        rec = std::move(mQueue.front());
        mQueue.pop_front();
      }
      if (std::fwrite(rec.data(), 1, rec.size(), stdout) != rec.size())
        std::_Exit(0); // MPC TOOLS has gone
      std::fflush(stdout);
    }
  }

  std::mutex mMutex;
  std::condition_variable mCv;
  std::deque<std::vector<std::uint8_t>> mQueue;
  bool mStop = false;
  std::thread mThread;
};

} // namespace

int main()
{
#if defined(_WIN32)
  _setmode(_fileno(stdout), _O_BINARY);
#endif
  std::setvbuf(stdout, nullptr, _IOFBF, 1 << 16);

  // Joining a Link session takes on the session's tempo, so this 120 never changes the MPC's.
  ableton::LinkAudio link(120.0, "MPC TOOLS");
  link.enable(true);
  link.enableLinkAudio(true);

  Writer writer;
  std::mutex sourceMutex;
  std::unique_ptr<ableton::LinkAudioSource> source;
  std::atomic<bool> quit{false};

  std::thread input(
    [&]
    {
      std::string line;
      while (std::getline(std::cin, line))
      {
        if (!line.empty() && line.back() == '\r')
          line.pop_back();
        if (line == "quit")
          break;
        if (line == "off")
        {
          std::lock_guard<std::mutex> lock(sourceMutex);
          source.reset();
          say("off");
        }
        else if (line.rfind("sub ", 0) == 0)
        {
          if (auto id = fromHex(line.substr(4)))
          {
            std::lock_guard<std::mutex> lock(sourceMutex);
            source.reset();
            source = std::make_unique<ableton::LinkAudioSource>(
              link,
              *id,
              [&writer](ableton::LinkAudioSource::BufferHandle h) { writer.push(h); });
            say("sub " + toHex(*id));
          }
          else
            say("error bad channel id");
        }
      }
      quit = true; // stdin closed: MPC TOOLS has gone or asked us to stop
    });

  std::string lastList;
  std::size_t lastPeers = static_cast<std::size_t>(-1);
  while (!quit)
  {
    const auto peers = link.numPeers();
    if (peers != lastPeers)
    {
      lastPeers = peers;
      say("peers " + std::to_string(peers));
    }
    std::ostringstream list;
    for (const auto& c : link.channels())
      list << "ch " << toHex(c.id) << '\t' << clean(c.peerName) << '\t' << clean(c.name)
           << '\n';
    if (list.str() != lastList)
    {
      lastList = list.str();
      std::lock_guard<std::mutex> lock(gErrMutex);
      std::fputs(("chs\n" + lastList + "end\n").c_str(), stderr);
      std::fflush(stderr);
    }
    std::this_thread::sleep_for(std::chrono::milliseconds(300));
  }

  {
    std::lock_guard<std::mutex> lock(sourceMutex);
    source.reset();
  }
  input.detach(); // still blocked in getline only if stdin is open, which it isn't any more
  link.enableLinkAudio(false);
  link.enable(false);
  std::_Exit(0);
}
