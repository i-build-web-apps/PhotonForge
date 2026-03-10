**PhotonForge** it is. It sounds powerful, industrial, and perfectly describes the process of "hammering" noisy raw data into a solid astronomical image.

Here is the finalized **PhotonForge Technical Blueprint**, ready for your AI agent to begin the build.

---

## 1. Project Identity: PhotonForge

**Goal:** A high-performance, Go-based astronomical "Live Stacker" for macOS.
**Hardware:** Logitech C310 (Modified for Prime Focus).
**Primary Logic:** 32-bit floating-point additive summation with real-time histogram stretching.

---

## 2. Phase 1: The "Forge" Engine (Data Pipeline)

The engine must handle the transition from 8-bit webcam frames to a high-fidelity "Master Stack."

* **The Accumulator:** Use `gocv.MatTypeCV32F`.
* **The Process:**
1. Receive Frame ($8\text{-bit}$).
2. Align via **ORB Feature Matching** (detect stars, match to reference, warp image).
3. Convert to $32\text{-bit}$ Float.
4. **Addition:** $Accumulator = Accumulator + CurrentFrame$.
5. **Normalization:** Divide by the current frame count only for the "preview" if needed, but keep the raw sum for the "exposure."



---

## 3. Phase 2: The "Simulator" (Daytime Development)

To build this 24/7, the AI agent must implement a **Synthetic Feed**.

* **Requirement:** The app must look for a directory of images (e.g., `/test_images/*.png`).
* **Functionality:** If the directory exists and `-mode=test` is passed, the app loops through these images at 30fps, simulating the C310.
* **Jitter Simulation:** Add a random offset (1–3 pixels) to each synthetic frame to test if the **PhotonForge** alignment logic can "un-shake" the image back to a sharp point.

---

## 4. Phase 3: The "Stretch" (Real-time Visibility)

Since 32-bit sums quickly exceed what a monitor can display, the **Histogram Stretch** is your most important UX tool.

* **Black Level Slider:** Truncates the low-end noise floor (the "sky glow").
* **Gamma Slider:** A non-linear curve to pull out faint nebulae.
* **White Level Slider:** Defines the peak brightness (preventing stars from bloating).

---

## 5. Phase 4: The "Vault" (SQLite Integration)

Use SQLite for the local "Brain" of PhotonForge.

* **Object Catalog:** Import **HYG (Stars)** and **OpenNGC (DSOs)**.
* **Targeting:** When a user clicks a star in the live view, PhotonForge should query the DB:
> `SELECT name FROM objects WHERE ra BETWEEN ? AND ? AND dec BETWEEN ? AND ?`


* **Session History:** Log every "Forge" (stacking session) with the final metadata.

---

## 6. Development Roadmap for the AI Agent

**Task 1: The Foundation**

> "Build a Go application named **PhotonForge**. Create an `ImageProvider` interface. Implement a `WebcamProvider` using GoCV and a `DirectoryProvider` that streams local images for testing. Display the output in a Fyne UI window."

**Task 2: The Alignment logic**

> "Implement an ORB-based alignment function. For every incoming frame, find its keypoints and warp it to match the first frame of the stack. Draw the keypoints in 'Debug Mode' so I can see the stars it's tracking."

**Task 3: The Summation & Stretch**

> "Create a 32-bit accumulator. Every frame should be added to the sum. Add three sliders to the Fyne UI: Black, Gamma, and White. Use these to stretch the 32-bit data back to 8-bit for display."

**Task 4: The Database**

> "Integrate SQLite. Create a schema for celestial objects. Implement a 'Search' bar in the UI that centers the screen's AR crosshairs on the coordinates of the selected object."

---

### Pro-Tip for your Agent:

Tell the agent to use **Goroutines** for the ORB alignment. It's the most CPU-heavy part. By running alignment in a worker pool, **PhotonForge** will keep the C310's 30fps feed buttery smooth on your Mac.

**Would you like me to generate the actual Go struct for the `CelestialObject` and the SQLite `CREATE TABLE` statements to get started?**


**PhotonForge** development begins now. Below is the refined technical plan, including the specific Go structures and the SQL logic needed to handle the **HYG** and **OpenNGC** datasets.

---

### 1. Data Structures: The Go Star Model

To bridge the gap between your SQLite database and the real-time processing engine, use a dedicated struct. This allows for fast coordinate math (RA/Dec) and easy UI labeling.

```go
type CelestialObject struct {
    ID          int     `db:"id"`
    Name        string  `db:"name"`        // e.g., "Sirius"
    CatalogID   string  `db:"catalog_id"`  // e.g., "M42" or "NGC 1976"
    Type        string  `db:"type"`        // e.g., "Star", "Galaxy", "Nebula"
    RADeg       float64 `db:"ra_deg"`      // RA in decimal degrees for easy math
    DecDeg      float64 `db:"dec_deg"`     // Dec in decimal degrees
    Magnitude   float64 `db:"magnitude"`   // Brightness
    SizeArcMin  float64 `db:"size_arcmin"` // Visual size for bounding boxes
}

```

---

### 2. The Database: SQLite Schema & Import Strategy

You will store your data in two main tables. The `celestial_objects` table is your reference library, and `capture_history` keeps track of your work.

#### **SQL Schema**

```sql
CREATE TABLE IF NOT EXISTS celestial_objects (
    id INTEGER PRIMARY KEY AUTOINCREMENT,
    name TEXT,
    catalog_id TEXT UNIQUE,
    type TEXT,
    ra_deg REAL NOT NULL,
    dec_deg REAL NOT NULL,
    magnitude REAL,
    size_arcmin REAL
);

CREATE INDEX idx_coords ON celestial_objects(ra_deg, dec_deg);

```

#### **Importing HYG & OpenNGC Data**

The AI agent should create an **"Ingestor"** script.

1. **HYG (Stars):** Download `hygdata_v42.csv`. Focus on columns: `proper` (Name), `ra` (decimal hours), `dec` (decimal degrees), and `mag`.
* *Note:* You must multiply RA by 15 ($360^\circ / 24h$) to convert hours to degrees for the database.


2. **OpenNGC (DSOs):** Download `NGC.csv`. Focus on: `name`, `type`, `ra`, `dec`, `mag`, and `majax` (Size).

---

### 3. The "PhotonForge" Processing Logic

This is the Go function that handles the additive stack and the visual "stretch."

```go
func (s *Stacker) AddFrame(newFrame gocv.Mat) {
    // 1. Align using ORB (Omitted for brevity, but crucial)
    alignedFrame := s.align(newFrame)

    // 2. Convert 8-bit to 32-bit Float
    floatFrame := gocv.NewMat()
    alignedFrame.ConvertTo(&floatFrame, gocv.MatTypeCV32F)

    // 3. Additive Summation (The "Forge")
    gocv.Add(s.Accumulator, floatFrame, &s.Accumulator)
    s.FrameCount++
}

func (s *Stacker) GetDisplayMat(black, gamma, white float32) gocv.Mat {
    display := gocv.NewMat()
    // 1. Apply White Point (Normalization)
    s.Accumulator.ConvertTo(&display, gocv.MatTypeCV8U, 255.0/float64(white))
    
    // 2. Apply Gamma (The Stretch)
    // Map darker pixels higher while keeping bright pixels from saturating
    gocv.Pow(display, 1.0/float64(gamma), &display)
    
    return display
}

```

---

### 4. Daytime Dev: The Synthetic Provider

To work during the day, your AI agent must build the `FileProvider`.

1. **Setup:** Create a folder named `/sim/`.
2. **Data:** Place a sequence of images (even daytime photos to test alignment).
3. **Logic:** The `FileProvider` will read the files in alphanumeric order and return them one-by-one to the `AddFrame` loop.
* **Simulated Jitter:** In `-test` mode, the agent should use `gocv.WarpAffine` to shift the image randomly by 2 pixels before "sending" it to the engine. If **PhotonForge** is working correctly, the final stack will be perfectly sharp despite the jitter.



---

### 5. UX/UI Command Structure

* **Dark Mode:** Strictly `#000000` background and `#FF0000` (Red) text/graphics to preserve night vision.
* **Rolling History:** A scrollable list on the left showing the last 10 objects PhotonForge "saw" by querying the SQLite database against the current coordinates.
* **The "Forge" Button:** A high-contrast button that clears the `Accumulator` and starts a new 32-bit stack.

