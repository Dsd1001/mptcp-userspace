import AppKit

let destination = CommandLine.arguments[1]
try FileManager.default.createDirectory(atPath: destination,withIntermediateDirectories:true)
for size in [16,32,64,128,256,512,1024] {
    let bitmap = NSBitmapImageRep(bitmapDataPlanes:nil,pixelsWide:size,pixelsHigh:size,bitsPerSample:8,samplesPerPixel:4,hasAlpha:true,isPlanar:false,colorSpaceName:.deviceRGB,bytesPerRow:0,bitsPerPixel:0)!
    let context = NSGraphicsContext(bitmapImageRep:bitmap)!
    NSGraphicsContext.saveGraphicsState();NSGraphicsContext.current=context
    let rect = NSRect(x:0,y:0,width:size,height:size)
    NSColor(calibratedRed:0.10,green:0.14,blue:0.15,alpha:1).setFill()
    NSBezierPath(roundedRect:rect.insetBy(dx:Double(size)*0.07,dy:Double(size)*0.07),xRadius:Double(size)*0.20,yRadius:Double(size)*0.20).fill()
    let config = NSImage.SymbolConfiguration(pointSize:Double(size)*0.55,weight:.medium)
        .applying(.init(paletteColors:[NSColor.systemTeal,NSColor.systemGreen]))
    let symbol = NSImage(systemSymbolName:"network",accessibilityDescription:nil)!.withSymbolConfiguration(config)!
    symbol.draw(in:rect.insetBy(dx:Double(size)*0.19,dy:Double(size)*0.19))
    NSGraphicsContext.restoreGraphicsState()
    let data = bitmap.representation(using:.png,properties:[:])!
    if size<=512 {try data.write(to:URL(fileURLWithPath:"\(destination)/icon_\(size)x\(size).png"))}
    if size>=32 {try data.write(to:URL(fileURLWithPath:"\(destination)/icon_\(size/2)x\(size/2)@2x.png"))}
}
