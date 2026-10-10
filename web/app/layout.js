import "./globals.css";

export const metadata = {
  title: "Sutra sync",
};

export default function RootLayout({ children }) {
  return (
    <html lang="en">
      <body>{children}</body>
    </html>
  );
}
