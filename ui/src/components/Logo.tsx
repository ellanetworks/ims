export default function Logo({
  width = 50,
  height = 50,
}: {
  width?: number;
  height?: number;
}) {
  return (
    <img
      src="/logo-mark.svg"
      alt="Ella IMS Logo"
      width={width}
      height={height}
    />
  );
}
