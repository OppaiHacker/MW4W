// Win32 bits mw4w.ps1 needs. Compiled once to cache\mw4w-native.dll.
using System;
using System.Collections.Generic;
using System.Drawing;
using System.Drawing.Drawing2D;
using System.Drawing.Imaging;
using System.Runtime.InteropServices;

namespace Mw4w
{
    public static class Native
    {
        [DllImport("user32.dll", CharSet = CharSet.Unicode)]
        static extern bool SystemParametersInfo(int action, int param, string value, int flags);

        [DllImport("user32.dll")]
        static extern uint GetWindowThreadProcessId(IntPtr hwnd, out uint pid);

        [DllImport("user32.dll")]
        static extern IntPtr GetForegroundWindow();

        [DllImport("powrprof.dll")]
        static extern bool SetSuspendState(bool hibernate, bool force, bool disableWake);

        [DllImport("shell32.dll")]
        static extern IntPtr SHAppBarMessage(uint msg, ref APPBARDATA data);

        [StructLayout(LayoutKind.Sequential)]
        struct APPBARDATA
        {
            public int cbSize; public IntPtr hWnd; public uint uCallbackMessage; public uint uEdge;
            public int left, top, right, bottom; public IntPtr lParam;
        }

        public static void SetWallpaper(string path)
        {
            // SPI_SETDESKWALLPAPER, SPIF_UPDATEINIFILE | SPIF_SENDCHANGE
            SystemParametersInfo(0x14, 0, path, 0x3);
        }

        public static uint PidOf(long hwnd)
        {
            uint pid;
            GetWindowThreadProcessId(hwnd == 0 ? GetForegroundWindow() : new IntPtr(hwnd), out pid);
            return pid;
        }

        public static void Suspend() { SetSuspendState(false, false, false); }

        public static void TaskbarAutoHide(bool on)
        {
            var d = new APPBARDATA();
            d.cbSize = Marshal.SizeOf(d);
            d.lParam = new IntPtr(on ? 1 : 2); // ABS_AUTOHIDE : ABS_ALWAYSONTOP
            SHAppBarMessage(0xA, ref d); // ABM_SETSTATE
        }

        public static bool TaskbarIsAutoHide()
        {
            var d = new APPBARDATA();
            d.cbSize = Marshal.SizeOf(d);
            return ((long)SHAppBarMessage(0x4, ref d) & 1) == 1; // ABM_GETSTATE
        }

        // ---------- thumbnails ----------
        public static void Thumb(string src, string dst, int w, int h)
        {
            using (var img = Image.FromFile(src))
            using (var bmp = new Bitmap(w, h))
            using (var g = Graphics.FromImage(bmp))
            {
                g.InterpolationMode = InterpolationMode.HighQualityBicubic;
                double s = Math.Max((double)w / img.Width, (double)h / img.Height);
                int dw = (int)(img.Width * s), dh = (int)(img.Height * s);
                g.DrawImage(img, (w - dw) / 2, (h - dh) / 2, dw, dh);
                bmp.Save(dst, ImageFormat.Jpeg);
            }
        }

        // ---------- dominant hue (pywal / matugen-ish) ----------
        // Returns "h,s,l" of the most prominent saturated colour, weighted by
        // saturation*value so muddy pixels don't win. Grey images give s=0.
        public static string Dominant(string path)
        {
            var bins = new double[36];
            var sat = new double[36];
            double lsum = 0; int n = 0;
            using (var img = Image.FromFile(path))
            using (var bmp = new Bitmap(img, 64, 36))
            {
                for (int y = 0; y < bmp.Height; y++)
                    for (int x = 0; x < bmp.Width; x++)
                    {
                        var c = bmp.GetPixel(x, y);
                        float hue = c.GetHue(), s = c.GetSaturation(), l = c.GetBrightness();
                        lsum += l; n++;
                        if (s < 0.2 || l < 0.12 || l > 0.92) continue;
                        double wgt = s * (1 - Math.Abs(l - 0.5) * 1.4);
                        int b = ((int)(hue / 10)) % 36;
                        bins[b] += wgt; sat[b] += s * wgt;
                    }
            }
            int best = 0;
            for (int i = 0; i < 36; i++)
            {
                // smooth over neighbours so a hue split between two bins still wins
                double a = bins[i] + 0.5 * (bins[(i + 35) % 36] + bins[(i + 1) % 36]);
                double bb = bins[best] + 0.5 * (bins[(best + 35) % 36] + bins[(best + 1) % 36]);
                if (a > bb) best = i;
            }
            double total = 0; foreach (var v in bins) total += v;
            if (total < 1) return "220,0," + (lsum / n).ToString("0.00", System.Globalization.CultureInfo.InvariantCulture);
            double avgSat = sat[best] / Math.Max(bins[best], 1e-9);
            return string.Format(System.Globalization.CultureInfo.InvariantCulture, "{0},{1:0.00},{2:0.00}", best * 10 + 5, avgSat, lsum / n);
        }

        // ---------- app icons via the shell (works for Win32 + UWP) ----------
        [ComImport, Guid("bcc18b79-ba16-442f-80c4-8a59c30c463b"), InterfaceType(ComInterfaceType.InterfaceIsIUnknown)]
        interface IShellItemImageFactory
        {
            [PreserveSig] int GetImage(SIZE size, int flags, out IntPtr phbm);
        }

        [StructLayout(LayoutKind.Sequential)]
        struct SIZE { public int cx, cy; }

        [DllImport("shell32.dll", CharSet = CharSet.Unicode, PreserveSig = false)]
        static extern void SHCreateItemFromParsingName(string path, IntPtr pbc, [MarshalAs(UnmanagedType.LPStruct)] Guid riid, [MarshalAs(UnmanagedType.Interface)] out IShellItemImageFactory item);

        [DllImport("gdi32.dll")]
        static extern bool DeleteObject(IntPtr h);

        // Saves PNGs for many apps in one process start. Returns count saved.
        public static int Icons(string[] appIds, string[] outFiles, int size)
        {
            int ok = 0;
            for (int i = 0; i < appIds.Length; i++)
            {
                try { if (Icon(appIds[i], outFiles[i], size)) ok++; } catch { }
            }
            return ok;
        }

        static bool Icon(string appId, string outFile, int size)
        {
            IShellItemImageFactory f;
            // App IDs from Get-StartApps, or a plain file path (exe of an open window).
            SHCreateItemFromParsingName(appId.Contains(":\\") ? appId : "shell:AppsFolder\\" + appId, IntPtr.Zero, typeof(IShellItemImageFactory).GUID, out f);
            IntPtr hbm;
            // SIIGBF_BIGGERSIZEOK | SIIGBF_ICONONLY
            if (f.GetImage(new SIZE { cx = size, cy = size }, 0x1 | 0x4, out hbm) != 0) return false;
            try
            {
                using (var src = Image.FromHbitmap(hbm))
                using (var bmp = WithAlpha(hbm, src.Width, src.Height))
                    bmp.Save(outFile, ImageFormat.Png);
                return true;
            }
            finally { DeleteObject(hbm); }
        }

        [DllImport("gdi32.dll")]
        static extern int GetObject(IntPtr h, int size, ref BITMAP bm);

        [StructLayout(LayoutKind.Sequential)]
        struct BITMAP { public int bmType, bmWidth, bmHeight, bmWidthBytes; public ushort bmPlanes, bmBitsPixel; public IntPtr bmBits; }

        // Image.FromHbitmap drops alpha; read the 32bpp DIB bits directly.
        static Bitmap WithAlpha(IntPtr hbm, int w, int h)
        {
            var bm = new BITMAP();
            GetObject(hbm, Marshal.SizeOf(bm), ref bm);
            if (bm.bmBitsPixel != 32 || bm.bmBits == IntPtr.Zero) return Image.FromHbitmap(hbm);
            var outBmp = new Bitmap(w, h, PixelFormat.Format32bppArgb);
            var data = outBmp.LockBits(new Rectangle(0, 0, w, h), ImageLockMode.WriteOnly, PixelFormat.Format32bppArgb);
            var row = new byte[bm.bmWidthBytes];
            for (int y = 0; y < h; y++)
            {
                // DIB sections from the shell are bottom-up.
                Marshal.Copy(new IntPtr(bm.bmBits.ToInt64() + (long)(h - 1 - y) * bm.bmWidthBytes), row, 0, row.Length);
                Marshal.Copy(row, 0, new IntPtr(data.Scan0.ToInt64() + (long)y * data.Stride), Math.Min(row.Length, data.Stride));
            }
            outBmp.UnlockBits(data);
            return outBmp;
        }
    }
    // Per-app volume for the media island: the Core Audio sessions of the default output
    // device (what the volume mixer shows), like MediaControll's PipeWire stream volume.
    public static class Audio
    {
        // Volume 0..100 of the first session whose process name is in procs (no .exe);
        // set >= 0 sets every matching session first. -1: the app has no audio session.
        public static int AppVolume(string[] procs, int set)
        {
            var en = (IMMDeviceEnumerator)new MMDeviceEnumerator();
            IMMDevice dev;
            if (en.GetDefaultAudioEndpoint(0, 1, out dev) != 0) return -1; // eRender, eMultimedia
            object o;
            var iid = typeof(IAudioSessionManager2).GUID;
            if (dev.Activate(ref iid, 0x17, IntPtr.Zero, out o) != 0) return -1; // CLSCTX_ALL
            IAudioSessionEnumerator list;
            if (((IAudioSessionManager2)o).GetSessionEnumerator(out list) != 0) return -1;
            int n;
            list.GetCount(out n);
            int result = -1;
            var ctx = Guid.Empty;
            for (int i = 0; i < n; i++)
            {
                IAudioSessionControl2 s;
                if (list.GetSession(i, out s) != 0) continue;
                uint pid;
                s.GetProcessId(out pid);
                if (pid == 0) continue; // system sounds
                string name;
                try { name = System.Diagnostics.Process.GetProcessById((int)pid).ProcessName; }
                catch { continue; }
                if (Array.FindIndex(procs, p => string.Equals(p, name, StringComparison.OrdinalIgnoreCase)) < 0) continue;
                var v = (ISimpleAudioVolume)s;
                if (set >= 0) v.SetMasterVolume(Math.Min(100, set) / 100f, ref ctx);
                float f;
                v.GetMasterVolume(out f);
                if (result < 0) result = (int)Math.Round(f * 100);
            }
            return result;
        }
    }

    [ComImport, Guid("BCDE0395-E52F-467C-8E3D-C4579291692E")]
    class MMDeviceEnumerator { }

    [ComImport, Guid("A95664D2-9614-4F35-A746-DE8DB63617E6"), InterfaceType(ComInterfaceType.InterfaceIsIUnknown)]
    interface IMMDeviceEnumerator
    {
        [PreserveSig] int EnumAudioEndpoints(int dataFlow, int stateMask, out IntPtr devices);
        [PreserveSig] int GetDefaultAudioEndpoint(int dataFlow, int role, out IMMDevice device);
    }

    [ComImport, Guid("D666063F-1587-4E43-81F1-B948E807363F"), InterfaceType(ComInterfaceType.InterfaceIsIUnknown)]
    interface IMMDevice
    {
        [PreserveSig] int Activate(ref Guid iid, int clsCtx, IntPtr activationParams, [MarshalAs(UnmanagedType.IUnknown)] out object iface);
    }

    // vtable order matters: IAudioSessionManager's two methods come first
    [ComImport, Guid("77AA99A0-1BD6-484F-8BC7-2C654C9A9B6F"), InterfaceType(ComInterfaceType.InterfaceIsIUnknown)]
    interface IAudioSessionManager2
    {
        [PreserveSig] int GetAudioSessionControl(IntPtr sessionGuid, int flags, out IntPtr control);
        [PreserveSig] int GetSimpleAudioVolume(IntPtr sessionGuid, int flags, out IntPtr volume);
        [PreserveSig] int GetSessionEnumerator(out IAudioSessionEnumerator list);
    }

    [ComImport, Guid("E2F5BB11-0570-40CA-ACDD-3AA01277DEE8"), InterfaceType(ComInterfaceType.InterfaceIsIUnknown)]
    interface IAudioSessionEnumerator
    {
        [PreserveSig] int GetCount(out int count);
        [PreserveSig] int GetSession(int index, out IAudioSessionControl2 session);
    }

    [ComImport, Guid("BFB7FF88-7239-4FC9-8FA2-07C950BE9C6D"), InterfaceType(ComInterfaceType.InterfaceIsIUnknown)]
    interface IAudioSessionControl2
    {
        // IAudioSessionControl
        [PreserveSig] int GetState(out int state);
        [PreserveSig] int GetDisplayName(out IntPtr name);
        [PreserveSig] int SetDisplayName(IntPtr name, IntPtr ctx);
        [PreserveSig] int GetIconPath(out IntPtr path);
        [PreserveSig] int SetIconPath(IntPtr path, IntPtr ctx);
        [PreserveSig] int GetGroupingParam(out Guid param);
        [PreserveSig] int SetGroupingParam(IntPtr param, IntPtr ctx);
        [PreserveSig] int RegisterAudioSessionNotification(IntPtr client);
        [PreserveSig] int UnregisterAudioSessionNotification(IntPtr client);
        // IAudioSessionControl2
        [PreserveSig] int GetSessionIdentifier(out IntPtr id);
        [PreserveSig] int GetSessionInstanceIdentifier(out IntPtr id);
        [PreserveSig] int GetProcessId(out uint pid);
    }

    [ComImport, Guid("87CE5498-68D6-44E5-9215-6DA47EF883D8"), InterfaceType(ComInterfaceType.InterfaceIsIUnknown)]
    interface ISimpleAudioVolume
    {
        [PreserveSig] int SetMasterVolume(float level, ref Guid ctx);
        [PreserveSig] int GetMasterVolume(out float level);
    }
}
