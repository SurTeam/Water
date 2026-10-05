// Preserve every native pasteboard type while exercising image paste.
import AppKit
import Foundation

let args = CommandLine.arguments
let board = NSPasteboard.general
switch args[1] {
case "save":
    let items = (board.pasteboardItems ?? []).map { item in
        Dictionary(uniqueKeysWithValues: item.types.compactMap { type in
            item.data(forType: type).map { (type.rawValue, $0.base64EncodedString()) }
        })
    }
    try JSONSerialization.data(withJSONObject: items).write(to: URL(fileURLWithPath: args[2]))
case "restore":
    let items = try JSONSerialization.jsonObject(with: Data(contentsOf: URL(fileURLWithPath: args[2]))) as! [[String: String]]
    board.clearContents()
    board.writeObjects(items.map { values in
        let item = NSPasteboardItem()
        for (type, value) in values { item.setData(Data(base64Encoded: value)!, forType: NSPasteboard.PasteboardType(type)) }
        return item
    })
case "image":
    let image = NSImage(contentsOfFile: args[2])!
    board.clearContents()
    board.writeObjects([image])
default:
    fatalError("unknown fixture action")
}
